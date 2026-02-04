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

package utils

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/awslabs/operatorpkg/serrors"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/linode/linodego"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	instancefilter "github.com/linode/karpenter-provider-linode/pkg/providers/instance/filter"
)

var (
	ErrInstanceNotFound = errors.New("instance not found")
	instanceIDRegex     = regexp.MustCompile(`(?P<Provider>.*)://(?P<InstanceID>.*)`)
)

// ParseInstanceID parses the provider ID stored on the node to get the instance ID
// associated with a node
func ParseInstanceID(providerID string) (string, error) {
	matches := instanceIDRegex.FindStringSubmatch(providerID)
	if matches == nil {
		return "", serrors.Wrap(fmt.Errorf("provider id does not match known format"), "provider-id", providerID)
	}
	for i, name := range instanceIDRegex.SubexpNames() {
		if name == "InstanceID" {
			return matches[i], nil
		}
	}
	return "", serrors.Wrap(fmt.Errorf("provider id does not match known format"), "provider-id", providerID)
}

// PrettySlice truncates a slice after a certain number of max items to ensure
// that the Slice isn't too long
func PrettySlice[T any](s []T, maxItems int) string {
	var sb strings.Builder
	for i, elem := range s {
		if i > maxItems-1 {
			fmt.Fprintf(&sb, " and %d other(s)", len(s)-i)
			break
		} else if i > 0 {
			fmt.Fprint(&sb, ", ")
		}
		fmt.Fprint(&sb, elem)
	}
	return sb.String()
}

// WithDefaultFloat64 returns the float64 value of the supplied environment variable or, if not present,
// the supplied default value. If the float64 conversion fails, returns the default
func WithDefaultFloat64(key string, def float64) float64 {
	val, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return def
	}
	return f
}

func GetTags(nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, clusterName string) map[string]string {
	// TODO: Validate tags
	// var invalidTags []string
	// if len(invalidTags) != 0 {
	// 	quotedTags := lo.Map(invalidTags, func(tag string, _ int) string {
	// 		return fmt.Sprintf("%q", tag)
	// 	})
	// 	return nil, serrors.Wrap(fmt.Errorf("tags failed validation requirements"), "tags", strings.Join(quotedTags, ", "))
	// }
	staticTags := map[string]string{
		fmt.Sprintf("kubernetes.io/cluster/%s", clusterName): "owned",
		karpv1.NodePoolLabelKey:                              nodeClaim.Labels[karpv1.NodePoolLabelKey],
		v1alpha1.LKEClusterNameTagKey:                        clusterName,
		v1alpha1.LabelNodeClass:                              nodeClass.Name,
	}

	return lo.Assign(TagListToMap(nodeClass.Spec.Tags), staticTags)
}

func GetTagsForLKE(nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, clusterName string) map[string]string {
	staticTags := map[string]string{
		fmt.Sprintf("kubernetes.io/cluster/%s", clusterName): "owned",
		karpv1.NodePoolLabelKey:                              nodeClaim.Labels[karpv1.NodePoolLabelKey],
		v1alpha1.LKEClusterNameTagKey:                        clusterName,
		v1alpha1.LabelNodeClass:                              nodeClass.Name,
		v1alpha1.LabelLKEManaged:                             "true",
	}

	return lo.Assign(TagListToMap(nodeClass.Spec.Tags), staticTags)
}

func GetInstanceTagsForLKE(nodeClaimName string) map[string]string {
	return map[string]string{
		v1alpha1.NodeClaimTagKey: nodeClaimName,
	}
}

func GetNodeClassHash(nodeClass *v1alpha1.LinodeNodeClass) string {
	return fmt.Sprintf("%s-%d", nodeClass.UID, nodeClass.Generation)
}

func TagListToMap(tags []string) map[string]string {
	tagMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		key, value, ok := splitTag(tag)
		if !ok {
			continue
		}
		tagMap[key] = value
	}
	return tagMap
}

func MapToTagList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s:%s", k, v))
	}
	return out
}

func DedupeTags(tags []string) []string {
	uniqueTagsSet := make(map[string]struct{}, len(tags))
	uniqueTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, exists := uniqueTagsSet[tag]; exists {
			continue
		}
		uniqueTagsSet[tag] = struct{}{}
		uniqueTags = append(uniqueTags, tag)
	}
	return uniqueTags
}

func splitTag(tag string) (string, string, bool) {
	if key, value, ok := splitTagWithSeparator(tag, ':'); ok {
		return key, value, true
	}
	return splitTagWithSeparator(tag, '=')
}

func splitTagWithSeparator(tag string, sep rune) (string, string, bool) {
	index := strings.IndexRune(tag, sep)
	if index <= 0 || index == len(tag)-1 {
		return "", "", false
	}
	return tag[:index], tag[index+1:], true
}

// LookupInstanceByTag returns the single Linode instance matching the provided tag.
func LookupInstanceByTag(ctx context.Context, client sdk.LinodeAPI, tag string) (*linodego.Instance, error) {
	filter := linodego.Filter{}
	filter.AddField(linodego.Contains, "tags", tag)
	filterJSON, err := filter.MarshalJSON()
	if err != nil {
		return nil, err
	}
	instances, err := client.ListInstances(ctx, linodego.NewListOptions(1, string(filterJSON)))
	if err != nil {
		return nil, err
	}
	if len(instances) == 1 {
		return &instances[0], nil
	}
	return nil, fmt.Errorf("instance tagged %q not found: %w", tag, ErrInstanceNotFound)
}

func IsRetryableError(err error) bool {
	if linodego.ErrHasStatus(err, http.StatusTooManyRequests) {
		return true
	}
	if linodego.ErrHasStatus(err,
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable) {
		return true
	}

	return false
}

// FilterInstanceTypes applies common filters to available instance types.
func FilterInstanceTypes(ctx context.Context, instanceTypes []*cloudprovider.InstanceType, nodeClaim *karpv1.NodeClaim) ([]*cloudprovider.InstanceType, error) {
	rejectedInstanceTypes := map[string][]*cloudprovider.InstanceType{}
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	for _, filter := range []instancefilter.Filter{
		instancefilter.CompatibleAvailableFilter(reqs, nodeClaim.Spec.Resources.Requests),
	} {
		remaining, rejected := filter.FilterReject(instanceTypes)
		if len(remaining) == 0 {
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("all requested instance types were unavailable during launch"))
		}
		if len(rejected) != 0 && filter.Name() != "compatible-available-filter" {
			rejectedInstanceTypes[filter.Name()] = rejected
		}
		instanceTypes = remaining
	}
	for filterName, its := range rejectedInstanceTypes {
		log.FromContext(ctx).WithValues("filter", filterName, "instance-types", PrettySlice(lo.Map(its, func(i *cloudprovider.InstanceType, _ int) string { return i.Name }), 5)).V(1).Info("filtered out instance types from launch")
	}
	return instanceTypes, nil
}

// CheapestInstanceType returns the lowest-price instance type from the set.
func CheapestInstanceType(instanceTypes []*cloudprovider.InstanceType) (*cloudprovider.InstanceType, error) {
	if len(instanceTypes) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no available instance types after filtering"))
	}
	cheapestType := instanceTypes[0]
	for _, it := range instanceTypes {
		if it.Offerings.Cheapest().Price < cheapestType.Offerings.Cheapest().Price {
			cheapestType = it
		}
	}
	return cheapestType, nil
}

func UpdateUnavailableOfferingsCache(
	ctx context.Context,
	err error,
	region string,
	instanceType *cloudprovider.InstanceType,
	unavailableOfferings *linodecache.UnavailableOfferings,

) {
	switch {
	case linodego.ErrHasStatus(err, http.StatusBadRequest):
		unavailableOfferings.MarkUnavailable(ctx, err.Error(), instanceType.Name, region)
	case linodego.ErrHasStatus(err,
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable):
		unavailableOfferings.MarkRegionUnavailable(region)
	case err != nil:
		log.FromContext(ctx).Error(err, "unexpected error during instance creation")
	}
}

// GetTagValue searches through a slice of tags and returns the value for the first tag
// that matches the key prefix format "key:value". If no matching tag is found,
// it returns an empty string and an error.
func GetTagValue(tags []string, key string) (string, error) {
	for _, tag := range tags {
		tagKey, value, ok := splitTag(tag)
		if ok && tagKey == key {
			return value, nil
		}
	}
	return "", fmt.Errorf("tag %s not found", key)
}
