/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhooks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

const (
	reconcileInterval = time.Hour
	retryInterval     = 30 * time.Second
)

// LinodeNodeClassCRDName is the CRD that serves multiple versions and therefore needs the conversion webhook.
var LinodeNodeClassCRDName = "linodenodeclasses." + apis.Group

// Config describes how the conversion webhook is exposed in the cluster.
type Config struct {
	// Namespace is the namespace of the Karpenter Service and certificate Secret.
	Namespace string
	// ServiceName is the Service fronting the webhook server. The certificate Secret is named <ServiceName>-cert.
	ServiceName string
	// Port is the port of the Service the apiserver calls.
	Port int
}

func (c Config) secretName() string {
	return c.ServiceName + "-cert"
}

func (c Config) dnsNames() []string {
	return []string{
		c.ServiceName,
		fmt.Sprintf("%s.%s", c.ServiceName, c.Namespace),
		fmt.Sprintf("%s.%s.svc", c.ServiceName, c.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", c.ServiceName, c.Namespace),
	}
}

func (c Config) validate() error {
	var errs []error
	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace is required"))
	}
	if c.ServiceName == "" {
		errs = append(errs, errors.New("service name is required"))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d is out of range", c.Port))
	}
	return errors.Join(errs...)
}

// CertManager keeps a self-signed serving certificate in a Secret and publishes its CA to the CRD conversion config.
// Every replica runs a CertManager: they converge on the same Secret contents via optimistic concurrency, so
// each replica serves a certificate that the apiserver trusts.
type CertManager struct {
	reader client.Reader
	writer client.Writer
	clock  clock.Clock
	config Config
	cert   atomic.Pointer[tls.Certificate]
}

var _ manager.Runnable = (*CertManager)(nil)
var _ manager.LeaderElectionRunnable = (*CertManager)(nil)

// NewCertManager returns a CertManager. reader should bypass any cache since the Secret and CRDs are not watched.
func NewCertManager(reader client.Reader, writer client.Writer, clk clock.Clock, config Config) *CertManager {
	return &CertManager{reader: reader, writer: writer, clock: clk, config: config}
}

// NeedLeaderElection returns false: the webhook is served by every replica.
func (m *CertManager) NeedLeaderElection() bool {
	return false
}

// GetCertificate implements tls.Config.GetCertificate.
func (m *CertManager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := m.cert.Load()
	if cert == nil {
		return nil, errors.New("webhook serving certificate is not available yet")
	}
	return cert, nil
}

// Start reconciles the certificate until ctx is done.
func (m *CertManager) Start(ctx context.Context) error {
	for {
		interval := reconcileInterval
		if err := m.Reconcile(ctx); err != nil {
			log.FromContext(ctx).Error(err, "failed to reconcile the webhook certificate")
			interval = retryInterval
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.clock.After(interval):
		}
	}
}

// Reconcile ensures a valid serving certificate exists, loads it for serving, and publishes the CA bundle to the CRDs.
func (m *CertManager) Reconcile(ctx context.Context) error {
	bundle, err := m.syncSecret(ctx)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(bundle.certPEM, bundle.keyPEM)
	if err != nil {
		return fmt.Errorf("loading serving key pair, %w", err)
	}
	m.cert.Store(&pair)
	return m.injectCABundle(ctx, bundle.caBundle)
}

func (m *CertManager) syncSecret(ctx context.Context) (*certBundle, error) {
	var bundle *certBundle
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := m.reader.Get(ctx, types.NamespacedName{Namespace: m.config.Namespace, Name: m.config.secretName()}, secret); err != nil {
			return fmt.Errorf("getting certificate secret, %w", err)
		}
		var changed bool
		var err error
		if bundle, changed, err = ensureCerts(secret.Data, m.config.dnsNames(), m.clock.Now()); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		for k, v := range bundle.data() {
			secret.Data[k] = v
		}
		return m.writer.Update(ctx, secret)
	})
	if err != nil {
		return nil, fmt.Errorf("syncing certificate secret, %w", err)
	}
	return bundle, nil
}

func (m *CertManager) injectCABundle(ctx context.Context, caBundle []byte) error {
	desired := conversionConfig(m.config, caBundle)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := m.reader.Get(ctx, types.NamespacedName{Name: LinodeNodeClassCRDName}, crd); err != nil {
			return fmt.Errorf("getting CRD %s, %w", LinodeNodeClassCRDName, err)
		}
		if equality.Semantic.DeepEqual(crd.Spec.Conversion, desired) {
			return nil
		}
		crd.Spec.Conversion = desired
		if err := m.writer.Update(ctx, crd); err != nil {
			return fmt.Errorf("updating conversion of CRD %s, %w", LinodeNodeClassCRDName, err)
		}
		log.FromContext(ctx).WithValues("crd", LinodeNodeClassCRDName).Info("configured conversion webhook")
		return nil
	})
}

func conversionConfig(config Config, caBundle []byte) *apiextensionsv1.CustomResourceConversion {
	path := ConversionPath
	port := int32(config.Port) //nolint:gosec // range is enforced by Config.validate
	return &apiextensionsv1.CustomResourceConversion{
		Strategy: apiextensionsv1.WebhookConverter,
		Webhook: &apiextensionsv1.WebhookConversion{
			ConversionReviewVersions: []string{"v1"},
			ClientConfig: &apiextensionsv1.WebhookClientConfig{
				Service: &apiextensionsv1.ServiceReference{
					Namespace: config.Namespace,
					Name:      config.ServiceName,
					Path:      &path,
					Port:      &port,
				},
				CABundle: caBundle,
			},
		},
	}
}
