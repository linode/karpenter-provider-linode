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
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

const (
	requeueAfterTime                    = 10 * time.Minute
	ConditionReasonDependenciesNotReady = "DependenciesNotReady"
	ConditionReasonTagValidationFailed  = "TagValidationFailed"
)

var ValidationConditionMessages = map[string]string{}

type Validation struct {
	kubeClient           client.Client
	cloudProvider        cloudprovider.CloudProvider
	linodeClient         sdk.LinodeAPI
	instanceTypeProvider instancetype.Provider
	cache                *cache.Cache
	dryRunDisabled       bool
}

func NewValidationReconciler(
	kubeClient client.Client,
	cloudProvider cloudprovider.CloudProvider,
	linodeClient sdk.LinodeAPI,
	instanceTypeProvider instancetype.Provider,
	cache *cache.Cache,
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

// nolint:gocyclo
func (v *Validation) Reconcile(ctx context.Context, nodeClass *v1.LinodeNodeClass) (reconcile.Result, error) {
	if _, ok := lo.Find(v.requiredConditions(), func(cond string) bool {
		return nodeClass.StatusConditions().Get(cond).IsFalse()
	}); ok {
		// If any of the required status conditions are false, we know validation will fail regardless of the other values.
		nodeClass.StatusConditions().SetFalse(
			v1.ConditionTypeValidationSucceeded,
			ConditionReasonDependenciesNotReady,
			"required status conditions are not satisfied",
		)
		return reconcile.Result{RequeueAfter: requeueAfterTime}, nil
	}
	if _, ok := lo.Find(v.requiredConditions(), func(cond string) bool {
		return nodeClass.StatusConditions().Get(cond).IsUnknown()
	}); ok {
		// If none of the status conditions are false, but at least one is unknown, we should also consider the validation
		// state to be unknown. Once all required conditions collapse to a true or false state, we can test validation.
		nodeClass.StatusConditions().SetUnknownWithReason(
			v1.ConditionTypeValidationSucceeded,
			ConditionReasonDependenciesNotReady,
			"required status conditions are not satisfied",
		)
		return reconcile.Result{RequeueAfter: requeueAfterTime}, nil
	}

	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Name: nodeClass.Name,
			},
		},
	}
	tags, err := utils.GetTags(nodeClass, nodeClaim, options.FromContext(ctx).ClusterID)
	if err != nil {
		nodeClass.StatusConditions().SetFalse(v1.ConditionTypeValidationSucceeded, ConditionReasonTagValidationFailed, err.Error())
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("validating tags, %w", err))
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

/* func (v *Validation) updateCacheOnFailure(nodeClass *v1.LinodeNodeClass, tags map[string]string, failureReason string) {
	v.cache.SetDefault(v.cacheKey(nodeClass, tags), failureReason)
	nodeClass.StatusConditions().SetFalse(
		v1.ConditionTypeValidationSucceeded,
		failureReason,
		ValidationConditionMessages[failureReason],
	)
} */

func (*Validation) requiredConditions() []string {
	return []string{}
}

func (*Validation) cacheKey(nodeClass *v1.LinodeNodeClass, tags map[string]string) string {
	hash := lo.Must(hashstructure.Hash([]any{
		nodeClass.Spec,
		nodeClass.Annotations,
		tags,
	}, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true}))
	return fmt.Sprintf("%s:%016x", nodeClass.Name, hash)
}

// clearCacheEntries removes all cache entries associated with the given nodeclass from the validation cache
func (v *Validation) clearCacheEntries(nodeClass *v1.LinodeNodeClass) {
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

// getInstanceTypesForNodeClass returns the set of instances which could be launched using this NodeClass based on the
// requirements of linked NodePools.
/* func (v *Validation) getInstanceTypesForNodeClass(ctx context.Context, nodeClass *v1.LinodeNodeClass) ([]*cloudprovider.InstanceType, error) {
	instanceTypes, err := v.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("listing instance types for nodeclass, %w", err)
	}
	nodePools, err := nodepoolutils.ListManaged(ctx, v.kubeClient, v.cloudProvider, nodepoolutils.ForNodeClass(nodeClass))
	if err != nil {
		return nil, fmt.Errorf("listing nodepools for nodeclass, %w", err)
	}
	var compatibleInstanceTypes []*cloudprovider.InstanceType
	names := sets.New[string]()
	for _, np := range nodePools {
		reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(np.Spec.Template.Spec.Requirements...)
		if np.Spec.Template.Labels != nil {
			reqs.Add(lo.Values(scheduling.NewLabelRequirements(np.Spec.Template.Labels))...)
		}
		for _, it := range instanceTypes {
			if it.Requirements.Intersects(reqs) != nil {
				continue
			}
			if names.Has(it.Name) {
				continue
			}
			names.Insert(it.Name)
			compatibleInstanceTypes = append(compatibleInstanceTypes, it)
		}
	}
	return compatibleInstanceTypes, nil
} */
