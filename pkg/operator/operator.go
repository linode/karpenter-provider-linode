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

package operator

import (
	"context"
	"fmt"
	"net"

	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/operator"

	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lkenode"
)

func init() {
}

// Operator is injected into the Linode CloudProvider's factories
type Operator struct {
	*operator.Operator
	UnavailableOfferingsCache *linodecache.UnavailableOfferings
	ValidationCache           *cache.Cache
	InstanceTypesProvider     *instancetype.DefaultProvider
	InstanceProvider          instance.Provider
	LKENodeProvider           lkenode.Provider
	LinodeClient              sdk.LinodeAPI
}

func NewOperator(ctx context.Context, operator *operator.Operator, linodeClientConfig sdk.ClientConfig) (context.Context, *Operator) {
	linodeClient := lo.Must(sdk.CreateLinodeClient(linodeClientConfig))
	unavailableOfferingsCache := linodecache.NewUnavailableOfferings()
	kubeDNSIP, err := KubeDNSIP(ctx, operator.KubernetesInterface)
	if err != nil {
		// If we fail to get the kube-dns IP, we don't want to crash because this causes issues with custom DNS setups
		log.FromContext(ctx).V(1).Info(fmt.Sprintf("unable to detect the IP of the kube-dns service, %s", err))
	} else {
		log.FromContext(ctx).WithValues("kube-dns-ip", kubeDNSIP).V(1).Info("discovered kube dns")
	}
	validationCache := cache.New(linodecache.ValidationTTL, linodecache.DefaultCleanupInterval)

	instanceTypeProvider := instancetype.NewDefaultProvider(
		linodeClient,
		instancetype.NewDefaultResolver(options.FromContext(ctx).ClusterRegion),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval),
		unavailableOfferingsCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	lo.Must0(instanceTypeProvider.UpdateInstanceTypes(ctx))
	lo.Must0(instanceTypeProvider.UpdateInstanceTypeOfferings(ctx))
	instanceProvider := instance.NewDefaultProvider(
		options.FromContext(ctx).ClusterRegion,
		operator.EventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval),
	)
	lkeNodeProvider := lkenode.NewDefaultProvider(
		options.FromContext(ctx).ClusterID,
		options.FromContext(ctx).ClusterRegion,
		operator.EventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval),
	)

	return ctx, &Operator{
		Operator:                  operator,
		UnavailableOfferingsCache: unavailableOfferingsCache,
		ValidationCache:           validationCache,
		InstanceTypesProvider:     instanceTypeProvider,
		InstanceProvider:          instanceProvider,
		LKENodeProvider:           lkeNodeProvider,
		LinodeClient:              linodeClient,
	}
}

func KubeDNSIP(ctx context.Context, kubernetesInterface kubernetes.Interface) (net.IP, error) {
	if kubernetesInterface == nil {
		return nil, fmt.Errorf("no K8s client provided")
	}
	dnsService, err := kubernetesInterface.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	kubeDNSIP := net.ParseIP(dnsService.Spec.ClusterIP)
	if kubeDNSIP == nil {
		return nil, fmt.Errorf("parsing cluster IP")
	}
	return kubeDNSIP, nil
}
