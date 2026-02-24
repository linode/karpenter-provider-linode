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

package test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	discoveryfake "k8s.io/client-go/discovery/fake"
	fakeK8s "k8s.io/client-go/kubernetes/fake"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	clock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/controllers/nodeoverlay"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/yaml"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
)

func init() {
	coretest.SetDefaultNodeClassType(&v1alpha1.LinodeNodeClass{})
}

type Environment struct {
	// Mock
	Clock             *clock.FakeClock
	EventRecorder     *coretest.EventRecorder
	InstanceTypeStore *nodeoverlay.InstanceTypeStore

	// API
	LinodeAPI *fake.LinodeClient

	// Cache
	LinodeCache               *cache.Cache
	InstanceTypeCache         *cache.Cache
	InstanceCache             *cache.Cache
	OfferingCache             *cache.Cache
	NodePoolCache             *cache.Cache
	UnavailableOfferingsCache *linodecache.UnavailableOfferings
	ValidationCache           *cache.Cache

	// Providers
	InstanceTypesProvider *instancetype.DefaultProvider
	InstanceProvider      *instance.DefaultProvider
	InstanceTypesResolver *instancetype.DefaultResolver
	LKENodeProvider       *lke.DefaultProvider
}

// NodeProvider returns the appropriate provider based on the mode in context.
// For tests that need a specific provider, use InstanceProvider or LKENodeProvider directly.
func (env *Environment) NodeProvider(ctx context.Context) instance.Provider {
	if options.FromContext(ctx).Mode == options.ProvisionModeLKE {
		return env.LKENodeProvider
	}
	return env.InstanceProvider
}

func NewEnvironment(ctx context.Context) *Environment {
	// Mock
	linodeClient := fake.NewLinodeClient()

	// cache
	linodeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceTypeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	nodePoolCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	discoveredCapacityCache := cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval)
	offeringCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	unavailableOfferingsCache := linodecache.NewUnavailableOfferings()
	validationCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	eventRecorder := coretest.NewEventRecorder()

	// Providers
	instanceTypesProvider := instancetype.NewDefaultProvider(
		linodeClient,
		instancetype.NewDefaultResolver(fake.DefaultRegion),
		instanceTypeCache,
		offeringCache,
		discoveredCapacityCache,
		unavailableOfferingsCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	lo.Must0(instanceTypesProvider.UpdateInstanceTypes(ctx))
	lo.Must0(instanceTypesProvider.UpdateInstanceTypeOfferings(ctx))
	// Build dummy cluster-info ConfigMap
	fakeClient := fakeK8s.NewClientset(fakeClusterInfoConfigMap("https://my-api-server:6443"))
	fakeDiscovery := fakeClient.Discovery().(*discoveryfake.FakeDiscovery)
	fakeDiscovery.FakedServerVersion = &version.Info{
		Major:      "1",
		Minor:      "34",
		GitVersion: "v1.34.4",
	}

	instanceProvider := instance.NewDefaultProvider(
		fake.DefaultRegion,
		eventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		instanceCache,
		fakeClient,
		fake.DefaultClusterID,
	)

	lkeNodeProvider := lke.NewDefaultProvider(
		fake.DefaultClusterID,
		fake.DefaultClusterTier,
		fake.DefaultClusterName,
		fake.DefaultRegion,
		eventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		nodePoolCache,
		lke.ProviderConfig{},
	)

	return &Environment{
		Clock:             &clock.FakeClock{},
		EventRecorder:     eventRecorder,
		InstanceTypeStore: nodeoverlay.NewInstanceTypeStore(),

		LinodeAPI: linodeClient,

		LinodeCache:               linodeCache,
		InstanceTypeCache:         instanceTypeCache,
		InstanceCache:             instanceCache,
		NodePoolCache:             nodePoolCache,
		OfferingCache:             offeringCache,
		UnavailableOfferingsCache: unavailableOfferingsCache,
		ValidationCache:           validationCache,

		InstanceTypesProvider: instanceTypesProvider,
		InstanceProvider:      instanceProvider,
		LKENodeProvider:       lkeNodeProvider,
	}
}

// generateTestCACert creates a minimal self-signed CA cert and returns its PEM bytes.
func generateTestCACert() []byte {
	key := lo.Must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	certDER := lo.Must(x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key))

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

func fakeClusterInfoConfigMap(server string) *v1.ConfigMap {
	kubeConfigObj := clientcmdapiv1.Config{
		Clusters: []clientcmdapiv1.NamedCluster{
			{
				Name: "local",
				Cluster: clientcmdapiv1.Cluster{
					Server:                   server,
					CertificateAuthorityData: generateTestCACert(), // valid PEM, not base64 here
				},
			},
		},
	}

	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-info",
			Namespace: "kube-public",
		},
		Data: map[string]string{
			"kubeconfig": string(lo.Must(yaml.Marshal(kubeConfigObj))),
		},
	}
}

func (env *Environment) Reset() {
	env.Clock.SetTime(time.Time{})

	env.LinodeAPI.Reset()

	env.LinodeCache.Flush()
	env.InstanceCache.Flush()
	env.NodePoolCache.Flush()
	env.UnavailableOfferingsCache.Flush()
	env.OfferingCache.Flush()
	env.ValidationCache.Flush()

	env.InstanceTypesProvider.Reset()

	mfs, err := crmetrics.Registry.Gather()
	if err != nil {
		for _, mf := range mfs {
			for _, metric := range mf.GetMetric() {
				if metric != nil {
					metric.Reset()
				}
			}
		}
	}
}

func (env *Environment) SetDefaults() {
	instances := fake.MakeInstances()
	env.LinodeAPI.ListTypesOutput.Set(ptr.To(instances))
	env.LinodeAPI.ListRegionsAvailabilityOutput.Set(ptr.To(fake.MakeInstanceOfferings(instances)))
}
