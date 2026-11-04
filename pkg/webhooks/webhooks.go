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

// Package webhooks serves the CRD conversion webhook that converts LinodeNodeClass objects between API versions.
package webhooks

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/apis/v1beta1"
)

// ConversionPath is the path the apiserver calls to convert custom resources.
const ConversionPath = "/convert"

// NewScheme returns a scheme with every LinodeNodeClass version registered, along with its conversions.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, v1beta1.AddToScheme} {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// NewConversionHandler returns the handler converting LinodeNodeClass between its versions.
func NewConversionHandler() (http.Handler, error) {
	s, err := NewScheme()
	if err != nil {
		return nil, fmt.Errorf("building conversion scheme, %w", err)
	}
	return conversion.NewWebhookHandler(s, conversion.NewRegistry()), nil
}

// Register adds the conversion webhook server and the certificate rotator to the manager. The rotator keeps a
// self-signed serving certificate in the Config's Secret and publishes its CA to the CRD, which must already
// declare the conversion webhook (the Helm chart does this).
func Register(mgr manager.Manager, config Config) error {
	if err := config.validate(); err != nil {
		return fmt.Errorf("invalid webhook config, %w", err)
	}
	handler, err := NewConversionHandler()
	if err != nil {
		return err
	}
	ready := make(chan struct{})
	// Every replica serves the webhook, so every replica rotates and injects certificates.
	if err := rotator.AddRotator(mgr, &rotator.CertRotator{
		SecretKey:            types.NamespacedName{Namespace: config.Namespace, Name: config.secretName()},
		CertDir:              CertDir,
		CAName:               "karpenter-linode-webhook-ca",
		CAOrganization:       "karpenter-linode",
		DNSName:              config.dnsName(),
		ExtraDNSNames:        config.extraDNSNames(),
		IsReady:              ready,
		EnableReadinessCheck: true,
		Webhooks:             []rotator.WebhookInfo{{Name: LinodeNodeClassCRDName, Type: rotator.CRDConversion}},
	}); err != nil {
		return fmt.Errorf("adding webhook certificate rotator, %w", err)
	}
	server := webhook.NewServer(webhook.Options{
		Port:    config.Port,
		CertDir: CertDir,
		TLSOpts: []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS12 }},
	})
	server.Register(ConversionPath, handler)
	if err := mgr.Add(&readyGate{ready: ready, server: server}); err != nil {
		return fmt.Errorf("adding webhook server, %w", err)
	}
	return nil
}

// readyGate starts the webhook server once its serving certificate is available, since the server
// fails to start without one.
type readyGate struct {
	ready  <-chan struct{}
	server manager.Runnable
}

var _ manager.LeaderElectionRunnable = (*readyGate)(nil)

func (g *readyGate) Start(ctx context.Context) error {
	select {
	case <-g.ready:
		return g.server.Start(ctx)
	case <-ctx.Done():
		return nil
	}
}

// NeedLeaderElection returns false: the webhook is served by every replica.
func (g *readyGate) NeedLeaderElection() bool {
	return false
}
