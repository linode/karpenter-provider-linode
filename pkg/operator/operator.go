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
	"os"
	"strconv"
	"time"

	"github.com/linode/linodego"
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
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

func init() {
}

// Operator is injected into the Linode CloudProvider's factories
type Operator struct {
	*operator.Operator
	UnavailableOfferingsCache *linodecache.UnavailableOfferings
	ValidationCache           *cache.Cache
	InstanceTypesProvider     *instancetype.DefaultProvider
	NodeProvider              instance.Provider
	LinodeClient              sdk.LinodeAPI
}

// allow passing a custom Linode client for testing
func NewOperator(ctx context.Context, operator *operator.Operator, linodeClient sdk.LinodeAPI) (*Operator, error) {
	if linodeClient == nil {
		linodeClient = lo.Must(sdk.CreateLinodeClient(lo.Must(CreateLinodeClientConfig(ctx))))
	}
	unavailableOfferingsCache := linodecache.NewUnavailableOfferings()
	kubeDNSIP, err := KubeDNSIP(ctx, operator.KubernetesInterface)
	if err != nil {
		// If we fail to get the kube-dns IP, we don't want to crash because this causes issues with custom DNS setups
		log.FromContext(ctx).V(1).Info(fmt.Sprintf("unable to detect the IP of the kube-dns service, %s", err))
	} else {
		log.FromContext(ctx).WithValues("kube-dns-ip", kubeDNSIP).V(1).Info("discovered kube dns")
	}
	validationCache := cache.New(linodecache.ValidationTTL, linodecache.DefaultCleanupInterval)

	opts := options.FromContext(ctx)
	var nodeProvider instance.Provider

	// Initialize only the provider needed for the configured mode
	switch opts.Mode {
	case "lke":
		log.FromContext(ctx).Info("initializing in LKE mode")
		listFilter := utils.Filter{Label: opts.ClusterName}
		filter, err := listFilter.String()
		if err != nil {
			return nil, err
		}
		// Get cluster ID from the cluster name (label)
		clusterList, err := linodeClient.ListLKEClusters(ctx, &linodego.ListOptions{
			Filter: filter,
		})
		if err != nil {
			return nil, err
		}
		if len(clusterList) != 1 {
			return nil, fmt.Errorf("could not determine LKE cluster with name: %s", opts.ClusterName)
		}

		if opts.ClusterRegion == "" {
			opts.ClusterRegion = clusterList[0].Region
			log.FromContext(ctx).WithValues("region", opts.ClusterRegion).Info("discovered LKE cluster region")
		}

		nodeProvider = lke.NewDefaultProvider(
			clusterList[0].ID,
			linodego.LKEVersionTier(clusterList[0].Tier),
			opts.ClusterName,
			opts.ClusterRegion,
			operator.EventRecorder,
			linodeClient,
			unavailableOfferingsCache,
			cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval),
		)
	case "instance":
		log.FromContext(ctx).Info("initializing in direct instance mode")
		nodeProvider = instance.NewDefaultProvider(
			opts.ClusterRegion,
			operator.EventRecorder,
			linodeClient,
			unavailableOfferingsCache,
			cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval),
		)
	}

	instanceTypeProvider := instancetype.NewDefaultProvider(
		linodeClient,
		instancetype.NewDefaultResolver(opts.ClusterRegion),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval),
		unavailableOfferingsCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	lo.Must0(instanceTypeProvider.UpdateInstanceTypes(ctx))
	lo.Must0(instanceTypeProvider.UpdateInstanceTypeOfferings(ctx))

	return &Operator{
		Operator:                  operator,
		UnavailableOfferingsCache: unavailableOfferingsCache,
		ValidationCache:           validationCache,
		InstanceTypesProvider:     instanceTypeProvider,
		NodeProvider:              nodeProvider,
		LinodeClient:              linodeClient,
	}, nil
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

func CreateLinodeClientConfig(ctx context.Context) (sdk.ClientConfig, error) {
	linodeToken := os.Getenv("LINODE_TOKEN")
	if linodeToken == "" {
		err := fmt.Errorf("LINODE_TOKEN environment variable is not set")
		log.FromContext(ctx).Error(err, "cannot start provider")
		return sdk.ClientConfig{}, err
	}
	linodeConfig := sdk.ClientConfig{Token: linodeToken}
	if raw, ok := os.LookupEnv("LINODE_CLIENT_TIMEOUT"); ok {
		if timeout, err := strconv.Atoi(raw); timeout > 0 && err == nil {
			linodeConfig.Timeout = time.Duration(timeout) * time.Second
		} else {
			log.FromContext(ctx).Info("LINODE_CLIENT_TIMEOUT environment variable is invalid, using default timeout")
		}
	}
	return linodeConfig, nil
}
