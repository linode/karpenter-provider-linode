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
	"net/http"

	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator"

	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
)

func init() {
	karpv1.NormalizedLabels = lo.Assign(karpv1.NormalizedLabels, map[string]string{"topology.ebs.csi.aws.com/zone": corev1.LabelTopologyZone})
}

// Operator is injected into the AWS CloudProvider's factories
type Operator struct {
	*operator.Operator
	InstanceTypesProvider *instancetype.DefaultProvider
	InstanceProvider      instance.Provider
	LinodeClient          linodego.Client
}

func NewOperator(ctx context.Context, operator *operator.Operator) (context.Context, *Operator) {
	linodeClient := linodego.NewClient(&http.Client{})
	unavailableOfferingsCache := linodecache.NewUnavailableOfferings()
	kubeDNSIP, err := KubeDNSIP(ctx, operator.KubernetesInterface)
	if err != nil {
		// If we fail to get the kube-dns IP, we don't want to crash because this causes issues with custom DNS setups
		// https://github.com/linode/karpenter-provider-linode/issues/2787
		log.FromContext(ctx).V(1).Info(fmt.Sprintf("unable to detect the IP of the kube-dns service, %s", err))
	} else {
		log.FromContext(ctx).WithValues("kube-dns-ip", kubeDNSIP).V(1).Info("discovered kube dns")
	}

	instanceTypeProvider := instancetype.NewDefaultProvider(
		&linodeClient,
		instancetype.NewDefaultResolver("dummy"),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.InstanceTypesZonesAndOfferingsTTL, linodecache.DefaultCleanupInterval),
		cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval),
		unavailableOfferingsCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	instanceProvider := instance.NewDefaultProvider(
		"dummy",
		operator.EventRecorder,
		&linodeClient,
		cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval),
	)

	return ctx, &Operator{
		Operator:              operator,
		InstanceTypesProvider: instanceTypeProvider,
		InstanceProvider:      instanceProvider,
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
