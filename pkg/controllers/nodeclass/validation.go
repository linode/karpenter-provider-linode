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

package nodeclass

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mitchellh/hashstructure/v2"
	gocache "github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

const (
	requeueAfterTime = 10 * time.Minute
)

var ValidationConditionMessages = map[string]string{}

type Validation struct {
	kubeClient           client.Client
	cloudProvider        cloudprovider.CloudProvider
	linodeClient         sdk.LinodeAPI
	instanceTypeProvider instancetype.Provider
	cache                *gocache.Cache
	dryRunDisabled       bool
}

func NewValidationReconciler(
	kubeClient client.Client,
	cloudProvider cloudprovider.CloudProvider,
	linodeClient sdk.LinodeAPI,
	instanceTypeProvider instancetype.Provider,
	cache *gocache.Cache,
	dryRunDisabled bool,
) *Validation {
	return &Validation{
		kubeClient:           kubeClient,
		cloudProvider:        cloudProvider,
		linodeClient:         linodeClient,
		instanceTypeProvider: instanceTypeProvider,
		cache:                cache,
		dryRunDisabled:       dryRunDisabled,
	}
}

func (v *Validation) Reconcile(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) (reconcile.Result, error) {
	nodeClaim := &v1.NodeClaim{
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Name: nodeClass.Name,
			},
		},
	}

	// Use appropriate tag function based on mode
	var tags map[string]string
	if options.FromContext(ctx).Mode == "lke" {
		tags = utils.GetTagsForLKE(nodeClass, nodeClaim, options.FromContext(ctx).ClusterName)
	} else {
		tags = utils.GetTags(nodeClass, nodeClaim, options.FromContext(ctx).ClusterName)
	}

	if val, ok := v.cache.Get(v.cacheKey(nodeClass, tags)); ok {
		// We still update the status condition even if it's cached since we may have had a conflict error previously
		if val == "" {
			nodeClass.StatusConditions().SetTrue(v1.ConditionTypeValidationSucceeded)
		} else {
			nodeClass.StatusConditions().SetFalse(
				v1.ConditionTypeValidationSucceeded,
				val.(string),
				ValidationConditionMessages[val.(string)],
			)
		}
		return reconcile.Result{RequeueAfter: requeueAfterTime}, nil
	}

	if v.dryRunDisabled {
		nodeClass.StatusConditions().SetTrue(v1.ConditionTypeValidationSucceeded)
		v.cache.SetDefault(v.cacheKey(nodeClass, tags), "")
		return reconcile.Result{RequeueAfter: requeueAfterTime}, nil
	}

	v.cache.SetDefault(v.cacheKey(nodeClass, tags), "")
	nodeClass.StatusConditions().SetTrue(v1.ConditionTypeValidationSucceeded)
	return reconcile.Result{RequeueAfter: requeueAfterTime}, nil
}

func (*Validation) cacheKey(nodeClass *v1alpha1.LinodeNodeClass, tags map[string]string) string {
	hash := lo.Must(hashstructure.Hash([]any{
		nodeClass.Spec,
		nodeClass.Annotations,
		tags,
	}, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true}))
	return fmt.Sprintf("%s:%016x", nodeClass.Name, hash)
}

// clearCacheEntries removes all cache entries associated with the given nodeclass from the validation cache
func (v *Validation) clearCacheEntries(nodeClass *v1alpha1.LinodeNodeClass) {
	var toDelete []string
	for key := range v.cache.Items() {
		parts := strings.Split(key, ":")
		// NOTE: should never occur, indicates malformed cache key
		if len(parts) != 2 {
			continue
		}
		if parts[0] == nodeClass.Name {
			toDelete = append(toDelete, key)
		}
	}
	for _, key := range toDelete {
		v.cache.Delete(key)
	}
}
