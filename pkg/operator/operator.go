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
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator"

	awscache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instanceprofile"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/providers/launchtemplate"
	"github.com/linode/karpenter-provider-linode/pkg/providers/pricing"
	"github.com/linode/karpenter-provider-linode/pkg/providers/version"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

func init() {
	karpv1.NormalizedLabels = lo.Assign(karpv1.NormalizedLabels, map[string]string{"topology.ebs.csi.aws.com/zone": corev1.LabelTopologyZone})
}

// Operator is injected into the AWS CloudProvider's factories
type Operator struct {
	*operator.Operator
	UnavailableOfferingsCache *awscache.UnavailableOfferings
	SSMCache                  *cache.Cache
	ValidationCache           *cache.Cache
	RecreationCache           *cache.Cache
	InstanceProfileProvider   instanceprofile.Provider
	LaunchTemplateProvider    launchtemplate.Provider
	PricingProvider           pricing.Provider
	VersionProvider           *version.DefaultProvider
	InstanceTypesProvider     *instancetype.DefaultProvider
	InstanceProvider          instance.Provider
	LinodeClient              *linodego.Client
}

func NewOperator(ctx context.Context, operator *operator.Operator) (context.Context, *Operator) {
	linodeAPI := linodego.NewClient(&http.Client{})
	kubeDNSIP, err := KubeDNSIP(ctx, operator.KubernetesInterface)
	if err != nil {
		// If we fail to get the kube-dns IP, we don't want to crash because this causes issues with custom DNS setups
		// https://github.com/linode/karpenter-provider-linode/issues/2787
		log.FromContext(ctx).V(1).Info(fmt.Sprintf("unable to detect the IP of the kube-dns service, %s", err))
	} else {
		log.FromContext(ctx).WithValues("kube-dns-ip", kubeDNSIP).V(1).Info("discovered kube dns")
	}
	unavailableOfferingsCache := awscache.NewUnavailableOfferings()
	ssmCache := cache.New(awscache.SSMCacheTTL, awscache.DefaultCleanupInterval)
	validationCache := cache.New(awscache.ValidationTTL, awscache.DefaultCleanupInterval)
	recreationCache := cache.New(awscache.RecreationTTL, awscache.DefaultCleanupInterval)

	versionProvider := version.NewDefaultProvider(operator.KubernetesInterface, linodeAPI)
	// Ensure we're able to hydrate the version before starting any reliant controllers.
	// Version updates are hydrated asynchronously after this, in the event of a failure
	// the previously resolved value will be used.
	lo.Must0(versionProvider.UpdateVersion(ctx))
	instanceTypeProvider := instancetype.NewDefaultProvider("", nil, &linodeAPI, cache.New(time.Minute*5, time.Minute))
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	instanceProvider := instance.NewDefaultProvider(ctx, "", nil, &linodeAPI, cache.New(time.Minute*5, time.Minute))

	// Setup field indexers on instanceID -- specifically for the interruption controller
	if options.FromContext(ctx).InterruptionQueue != "" {
		SetupIndexers(ctx, operator.Manager)
	}
	return ctx, &Operator{
		Operator:                  operator,
		UnavailableOfferingsCache: unavailableOfferingsCache,
		SSMCache:                  ssmCache,
		ValidationCache:           validationCache,
		RecreationCache:           recreationCache,
		VersionProvider:           versionProvider,
		InstanceTypesProvider:     instanceTypeProvider,
		InstanceProvider:          instanceProvider,
	}
}

func GetCABundle(ctx context.Context, restConfig *rest.Config) (*string, error) {
	// Discover CA Bundle from the REST client. We could alternatively
	// have used the simpler client-go InClusterConfig() method.
	// However, that only works when Karpenter is running as a Pod
	// within the same cluster it's managing.
	if caBundle := options.FromContext(ctx).ClusterCABundle; caBundle != "" {
		return lo.ToPtr(caBundle), nil
	}
	transportConfig, err := restConfig.TransportConfig()
	if err != nil {
		return nil, fmt.Errorf("discovering caBundle, loading transport config, %w", err)
	}
	_, err = transport.TLSConfigFor(transportConfig) // fills in CAData!
	if err != nil {
		return nil, fmt.Errorf("discovering caBundle, loading TLS config, %w", err)
	}
	return lo.ToPtr(base64.StdEncoding.EncodeToString(transportConfig.TLS.CAData)), nil
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

func SetupIndexers(ctx context.Context, mgr manager.Manager) {
	lo.Must0(mgr.GetFieldIndexer().IndexField(ctx, &karpv1.NodeClaim{}, "status.instanceID", func(o client.Object) []string {
		if o.(*karpv1.NodeClaim).Status.ProviderID == "" {
			return nil
		}
		id, e := utils.ParseInstanceID(o.(*karpv1.NodeClaim).Status.ProviderID)
		if e != nil || id == "" {
			return nil
		}
		return []string{id}
	}), "failed to setup nodeclaim instanceID indexer")
	lo.Must0(mgr.GetFieldIndexer().IndexField(ctx, &corev1.Node{}, "spec.instanceID", func(o client.Object) []string {
		if o.(*corev1.Node).Spec.ProviderID == "" {
			return nil
		}
		id, e := utils.ParseInstanceID(o.(*corev1.Node).Spec.ProviderID)
		if e != nil || id == "" {
			return nil
		}
		return []string{id}
	}), "failed to setup node instanceID indexer")
}
