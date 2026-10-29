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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/linode/linodego/v2"
	"github.com/mitchellh/hashstructure/v2"
	gocache "github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

const (
	requeueAfterTime = 10 * time.Minute
	// Requeue invalid LKE cluster tier checks (lkeK8sVersion, firewallID) quickly so readiness flips
	// soon after an external LKE cluster change becomes visible in the Linode API.
	lkeTierRequeueAfterTime = time.Minute
)

const (
	ConditionReasonTagValidationFailed               = "TagValidationFailed"
	ConditionReasonLKEK8sVersionUnsupported          = "LKEK8sVersionUnsupported"
	ConditionReasonLKEK8sVersionControlPlaneMismatch = "LKEK8sVersionControlPlaneMismatch"
	ConditionReasonLKEFirewallIDUnsupported          = "LKEFirewallIDUnsupported"
	ConditionReasonLKEEnterpriseFieldsUnsupported    = "LKEEnterpriseFieldsUnsupported"
	ConditionReasonUnsupportedInstanceFields         = "UnsupportedInstanceFields"
	ConditionReasonUnsupportedLKEFields              = "UnsupportedLKEFields"
)

var ValidationConditionMessages = map[string]string{
	ConditionReasonTagValidationFailed:               "LinodeNodeClass tags violate provider-managed key or 50-character length requirements",
	ConditionReasonLKEK8sVersionUnsupported:          "lkeK8sVersion is only supported for LKE Enterprise clusters",
	ConditionReasonLKEK8sVersionControlPlaneMismatch: "lkeK8sVersion requires the LKE cluster control plane to already be on the same version",
	ConditionReasonLKEFirewallIDUnsupported:          "firewallID is only supported for LKE Enterprise clusters",
	ConditionReasonLKEEnterpriseFieldsUnsupported:    "lkeK8sVersion and firewallID are only supported for LKE Enterprise clusters",
	ConditionReasonUnsupportedInstanceFields:         "LinodeNodeClass sets instance-only fields that are not supported in LKE mode",
	ConditionReasonUnsupportedLKEFields:              "LinodeNodeClass sets LKE-only fields that are not supported in instance mode",
}

type Validation struct {
	kubeClient           client.Client
	cloudProvider        cloudprovider.CloudProvider
	recorder             events.Recorder
	clusterID            int
	linodeClient         sdk.LinodeAPI
	instanceTypeProvider instancetype.Provider
	cache                *gocache.Cache
	dryRunDisabled       bool
}

func NewValidationReconciler(
	kubeClient client.Client,
	cloudProvider cloudprovider.CloudProvider,
	recorder events.Recorder,
	clusterID int,
	linodeClient sdk.LinodeAPI,
	instanceTypeProvider instancetype.Provider,
	cache *gocache.Cache,
	dryRunDisabled bool,
) *Validation {
	return &Validation{
		kubeClient:           kubeClient,
		cloudProvider:        cloudProvider,
		recorder:             recorder,
		clusterID:            clusterID,
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

	// The NodePool name is unavailable here; the LKE provider validates that concrete tag before any API call.
	var tags map[string]string
	if options.FromContext(ctx).Mode == "lke" {
		tags = utils.GetTagsForLKE(nodeClass, nodeClaim, options.FromContext(ctx).ClusterName)
	} else {
		tags = utils.GetTags(nodeClass, nodeClaim, options.FromContext(ctx).ClusterName)
	}

	if err := v.validateTags(nodeClass, tags); err != nil {
		return reconcile.Result{}, err
	}
	if err := v.validateLKEUnsupportedFields(ctx, nodeClass); err != nil {
		return reconcile.Result{}, err
	}
	if err := v.validateInstanceUnsupportedFields(ctx, nodeClass); err != nil {
		return reconcile.Result{}, err
	}
	if res, err := v.validateLKEClusterTier(ctx, nodeClass); err != nil || !lo.IsEmpty(res) {
		return res, err
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

func (v *Validation) validateTags(nodeClass *v1alpha1.LinodeNodeClass, generatedTags map[string]string) error {
	err := errors.Join(
		utils.ValidateTags(nodeClass.Spec.Tags),
		utils.ValidateTagLengths(nodeClass.Spec.Tags),
		utils.ValidateTagLengths(utils.MapToTagList(generatedTags)),
	)
	if err != nil {
		nodeClass.StatusConditions().SetFalse(v1.ConditionTypeValidationSucceeded, ConditionReasonTagValidationFailed, err.Error())
		return reconcile.TerminalError(fmt.Errorf("validating tags, %w", err))
	}
	return nil
}

// validateLKEUnsupportedFields rejects LinodeNodeClasses that set instance-only fields while
// running in LKE mode. In LKE mode only LKE node pools are provisioned, which cannot consume
// these instance-only fields, so silently ignoring them would be surprising. The fields remain
// in the API for the eventual direct-instance mode; this guard only fires when Mode is "lke".
func (v *Validation) validateLKEUnsupportedFields(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) error {
	if options.FromContext(ctx).Mode != "lke" {
		return nil
	}

	spec := nodeClass.Spec
	var setFields []string
	if len(spec.AuthorizedKeys) > 0 {
		setFields = append(setFields, "authorizedKeys")
	}
	if len(spec.AuthorizedUsers) > 0 {
		setFields = append(setFields, "authorizedUsers")
	}
	if spec.Image != "" {
		setFields = append(setFields, "image")
	}
	if spec.BackupsEnabled {
		setFields = append(setFields, "backupsEnabled")
	}
	if spec.OSDisk != nil {
		setFields = append(setFields, "osDisk")
	}
	if spec.DataDisks != nil {
		setFields = append(setFields, "dataDisks")
	}
	if spec.Configuration != nil {
		setFields = append(setFields, "configuration")
	}
	if spec.PlacementGroup != nil {
		setFields = append(setFields, "placementGroup")
	}
	if spec.VPCID != nil {
		setFields = append(setFields, "vpcID")
	}
	if spec.IPv6Options != nil {
		setFields = append(setFields, "ipv6Options")
	}
	if spec.SwapSize != nil {
		setFields = append(setFields, "swapSize")
	}
	if spec.DiskEncryption != "" {
		setFields = append(setFields, "diskEncryption")
	}

	if len(setFields) == 0 {
		return nil
	}

	nodeClass.StatusConditions().SetFalse(
		v1.ConditionTypeValidationSucceeded,
		ConditionReasonUnsupportedInstanceFields,
		fmt.Sprintf("%s: %s", ValidationConditionMessages[ConditionReasonUnsupportedInstanceFields], strings.Join(setFields, ", ")),
	)
	return reconcile.TerminalError(fmt.Errorf("validating nodeclass: instance-only fields are not supported in LKE mode: %s", strings.Join(setFields, ", ")))
}

// validateInstanceUnsupportedFields rejects LinodeNodeClasses that set LKE-only fields while
// running in instance mode, where they would otherwise be silently ignored.
func (v *Validation) validateInstanceUnsupportedFields(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) error {
	if options.FromContext(ctx).Mode != "instance" {
		return nil
	}

	var setFields []string
	if nodeClass.Spec.LKEK8sVersion != nil {
		setFields = append(setFields, "lkeK8sVersion")
	}
	if len(setFields) == 0 {
		return nil
	}

	nodeClass.StatusConditions().SetFalse(
		v1alpha1.ConditionTypeValidationSucceeded,
		ConditionReasonUnsupportedLKEFields,
		fmt.Sprintf("%s: %s", ValidationConditionMessages[ConditionReasonUnsupportedLKEFields], strings.Join(setFields, ", ")),
	)
	return reconcile.TerminalError(fmt.Errorf("validating nodeclass: LKE-only fields are not supported in instance mode: %s", strings.Join(setFields, ", ")))
}

// validateLKEClusterTier validates fields that depend on the LKE cluster tier (lkeK8sVersion and
// firewallID), fetching the cluster at most once.
func (v *Validation) validateLKEClusterTier(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) (reconcile.Result, error) {
	if options.FromContext(ctx).Mode != "lke" || (nodeClass.Spec.LKEK8sVersion == nil && nodeClass.Spec.FirewallID == nil) {
		return reconcile.Result{}, nil
	}
	if v.clusterID == 0 {
		return reconcile.Result{}, fmt.Errorf("validating LKE cluster tier fields, LKE cluster ID is not configured")
	}

	cluster, err := v.linodeClient.GetLKECluster(ctx, v.clusterID)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("getting LKE cluster %d: %w", v.clusterID, err)
	}

	switch linodego.LKEVersionTier(cluster.Tier) {
	case linodego.LKEVersionEnterprise:
		if nodeClass.Spec.LKEK8sVersion == nil {
			return reconcile.Result{}, nil
		}
		desiredVersion := *nodeClass.Spec.LKEK8sVersion
		if cluster.K8sVersion == desiredVersion {
			return reconcile.Result{}, nil
		}
		message := fmt.Sprintf("lkeK8sVersion %q requires the LKE cluster control plane to already be on %q, current cluster version is %q", desiredVersion, desiredVersion, cluster.K8sVersion)
		return v.failLKEClusterTierValidation(nodeClass, ConditionReasonLKEK8sVersionControlPlaneMismatch, message), nil
	case linodego.LKEVersionStandard:
		reason := ConditionReasonLKEEnterpriseFieldsUnsupported
		switch {
		case nodeClass.Spec.LKEK8sVersion != nil && nodeClass.Spec.FirewallID == nil:
			reason = ConditionReasonLKEK8sVersionUnsupported
		case nodeClass.Spec.LKEK8sVersion == nil && nodeClass.Spec.FirewallID != nil:
			reason = ConditionReasonLKEFirewallIDUnsupported
		}
		return v.failLKEClusterTierValidation(nodeClass, reason, ValidationConditionMessages[reason]), nil
	}
	return reconcile.Result{}, fmt.Errorf("validating LKE cluster tier fields, unsupported LKE version tier %q", cluster.Tier)
}

func (v *Validation) failLKEClusterTierValidation(nodeClass *v1alpha1.LinodeNodeClass, reason, message string) reconcile.Result {
	nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionTypeValidationSucceeded, reason, message)
	v.recorder.Publish(LKEValidationFailedEvent(nodeClass, reason, message))
	return reconcile.Result{RequeueAfter: lkeTierRequeueAfterTime}
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
