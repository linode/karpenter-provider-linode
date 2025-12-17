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

package instance

import (
	"context"
	"fmt"
	"strconv"

	"github.com/awslabs/operatorpkg/option"
	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

type Provider interface {
	Create(context.Context, *v1.LinodeNodeClass, *karpv1.NodeClaim, map[string]string, []*cloudprovider.InstanceType) (*Instance, error)
	Get(context.Context, string, ...Options) (*Instance, error)
	List(context.Context) ([]*Instance, error)
	Delete(context.Context, string) error
	CreateTags(context.Context, string, map[string]string) error
}

type options struct {
	SkipCache bool
}

type Options = option.Function[options]

var SkipCache = func(opts *options) {
	opts.SkipCache = true
}

type DefaultProvider struct {
	region        string
	recorder      events.Recorder
	client        *linodego.Client
	instanceCache *cache.Cache
}

func NewDefaultProvider(
	region string,
	recorder events.Recorder,
	client *linodego.Client,
	instanceCache *cache.Cache,
) *DefaultProvider {

	return &DefaultProvider{
		region:        region,
		recorder:      recorder,
		client:        client,
		instanceCache: instanceCache,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*Instance, error) {
	// Merge tags from NodeClaim and LinodeNodeClass
	tagList := nodeClass.Spec.Tags
	for k, v := range tags {
		tagList = append(tagList, fmt.Sprintf("%s:%s", k, v))
	}

	// Deduplicate tags
	uniqueTagsSet := make(map[string]struct{})
	for _, tag := range tagList {
		uniqueTagsSet[tag] = struct{}{}
	}
	uniqueTags := make([]string, len(uniqueTagsSet))
	for tag := range uniqueTagsSet {
		uniqueTags = append(uniqueTags, tag)
	}

	createOpts := linodego.InstanceCreateOptions{
		Region:          p.region,
		Type:            nodeClass.Spec.Type,
		RootPass:        nodeClass.Spec.RootPass,
		AuthorizedKeys:  nodeClass.Spec.AuthorizedKeys,
		AuthorizedUsers: nodeClass.Spec.AuthorizedUsers,
		Image:           nodeClass.Spec.Image,
		BackupsEnabled:  nodeClass.Spec.BackupsEnabled,
		PrivateIP:       nodeClass.Spec.PrivateIP,
		NetworkHelper:   nodeClass.Spec.NetworkHelper,
		Tags:            uniqueTags,
		FirewallID:      nodeClass.Spec.FirewallID,
		// NOTE: Linode Interfaces may not currently be available to all users.
		LinodeInterfaces: constructLinodeInterfaceCreateOpts(nodeClass.Spec.LinodeInterfaces),
		// NOTE: Disk encryption may not currently be available to all users.
		DiskEncryption: nodeClass.Spec.DiskEncryption,
		PlacementGroup: (*linodego.InstanceCreatePlacementGroupOptions)(nodeClass.Spec.PlacementGroup),
		SwapSize:       nodeClass.Spec.SwapSize,
	}

	if nodeClass.Spec.PlacementGroup != nil {
		createOpts.PlacementGroup = &linodego.InstanceCreatePlacementGroupOptions{
			ID:            nodeClass.Spec.PlacementGroup.ID,
			CompliantOnly: nodeClass.Spec.PlacementGroup.CompliantOnly,
		}
	}

	instance, err := p.client.CreateInstance(ctx, createOpts)
	if err != nil {
		return nil, err
	}

	return NewInstance(ctx, *instance), nil
}

// Unfortunately, this is necessary since DeepCopy can't be generated for linodego.LinodeInterfaceCreateOptions
// so here we manually create the options for Linode interfaces.
func constructLinodeInterfaceCreateOpts(createOpts []v1.LinodeInterfaceCreateOptions) []linodego.LinodeInterfaceCreateOptions {
	linodeInterfaces := make([]linodego.LinodeInterfaceCreateOptions, len(createOpts))
	for idx, iface := range createOpts {
		ifaceCreateOpts := linodego.LinodeInterfaceCreateOptions{}
		// Handle VLAN
		if iface.VLAN != nil {
			ifaceCreateOpts.VLAN = &linodego.VLANInterface{
				VLANLabel:   iface.VLAN.VLANLabel,
				IPAMAddress: iface.VLAN.IPAMAddress,
			}
		}
		// Handle VPC
		if iface.VPC != nil {
			ifaceCreateOpts.VPC = constructLinodeInterfaceVPC(iface)
		}
		// Handle Public Interface
		if iface.Public != nil {
			ifaceCreateOpts.Public = constructLinodeInterfacePublic(iface)
		}
		// Handle Default Route
		if iface.DefaultRoute != nil {
			ifaceCreateOpts.DefaultRoute = &linodego.InterfaceDefaultRoute{
				IPv4: iface.DefaultRoute.IPv4,
				IPv6: iface.DefaultRoute.IPv6,
			}
		}
		ifaceCreateOpts.FirewallID = ptr.To(iface.FirewallID)
		// createOpts is now fully populated with the interface options
		linodeInterfaces[idx] = ifaceCreateOpts
	}

	return linodeInterfaces
}

// constructLinodeInterfaceVPC constructs a Linode VPC interface configuration from the provided LinodeInterfaceCreateOptions.
func constructLinodeInterfaceVPC(iface v1.LinodeInterfaceCreateOptions) *linodego.VPCInterfaceCreateOptions {
	var (
		ipv4Addrs    []linodego.VPCInterfaceIPv4AddressCreateOptions
		ipv4Ranges   []linodego.VPCInterfaceIPv4RangeCreateOptions
		ipv6Ranges   []linodego.VPCInterfaceIPv6RangeCreateOptions
		ipv6SLAAC    []linodego.VPCInterfaceIPv6SLAACCreateOptions
		ipv6IsPublic bool
	)
	if iface.VPC.IPv4 != nil {
		for _, addr := range iface.VPC.IPv4.Addresses {
			ipv4Addrs = append(ipv4Addrs, linodego.VPCInterfaceIPv4AddressCreateOptions{
				Address:        ptr.To(addr.Address),
				Primary:        addr.Primary,
				NAT1To1Address: addr.NAT1To1Address,
			})
		}
		for _, rng := range iface.VPC.IPv4.Ranges {
			ipv4Ranges = append(ipv4Ranges, linodego.VPCInterfaceIPv4RangeCreateOptions{
				Range: rng.Range,
			})
		}
	} else {
		// If no IPv4 addresses are specified, we set a default NAT1To1 address to "any"
		ipv4Addrs = []linodego.VPCInterfaceIPv4AddressCreateOptions{
			{
				Primary:        ptr.To(true),
				NAT1To1Address: ptr.To("auto"),
				Address:        ptr.To("auto"), // Default to auto-assigned address
			},
		}
	}
	if iface.VPC.IPv6 != nil {
		for _, slaac := range iface.VPC.IPv6.SLAAC {
			ipv6SLAAC = append(ipv6SLAAC, linodego.VPCInterfaceIPv6SLAACCreateOptions{
				Range: slaac.Range,
			})
		}
		for _, rng := range iface.VPC.IPv6.Ranges {
			ipv6Ranges = append(ipv6Ranges, linodego.VPCInterfaceIPv6RangeCreateOptions{
				Range: rng.Range,
			})
		}
		if iface.VPC.IPv6.IsPublic != nil {
			ipv6IsPublic = *iface.VPC.IPv6.IsPublic
		}
	}
	subnetID := 0
	if iface.VPC.SubnetID != nil {
		subnetID = *iface.VPC.SubnetID
	}
	return &linodego.VPCInterfaceCreateOptions{
		SubnetID: subnetID,
		IPv4: &linodego.VPCInterfaceIPv4CreateOptions{
			Addresses: &ipv4Addrs,
			Ranges:    &ipv4Ranges,
		},
		IPv6: &linodego.VPCInterfaceIPv6CreateOptions{
			SLAAC:    &ipv6SLAAC,
			Ranges:   &ipv6Ranges,
			IsPublic: &ipv6IsPublic,
		},
	}
}

// constructLinodeInterfacePublic constructs a Linode Public interface configuration from the provided LinodeInterfaceCreateOptions.
func constructLinodeInterfacePublic(iface v1.LinodeInterfaceCreateOptions) *linodego.PublicInterfaceCreateOptions {
	var (
		ipv4Addrs  []linodego.PublicInterfaceIPv4AddressCreateOptions
		ipv6Ranges []linodego.PublicInterfaceIPv6RangeCreateOptions
	)
	if iface.Public.IPv4 != nil {
		for _, addr := range iface.Public.IPv4.Addresses {
			ipv4Addrs = append(ipv4Addrs, linodego.PublicInterfaceIPv4AddressCreateOptions{
				Address: ptr.To(addr.Address),
				Primary: addr.Primary,
			})
		}
	}
	if iface.Public.IPv6 != nil {
		for _, rng := range iface.Public.IPv6.Ranges {
			ipv6Ranges = append(ipv6Ranges, linodego.PublicInterfaceIPv6RangeCreateOptions{
				Range: rng.Range,
			})
		}
	}
	return &linodego.PublicInterfaceCreateOptions{
		IPv4: &linodego.PublicInterfaceIPv4CreateOptions{
			Addresses: &ipv4Addrs,
		},
		IPv6: &linodego.PublicInterfaceIPv6CreateOptions{
			Ranges: &ipv6Ranges,
		},
	}
}

func (p *DefaultProvider) Get(ctx context.Context, id string, opts ...Options) (*Instance, error) {
	skipCache := option.Resolve(opts...).SkipCache
	if !skipCache {
		if i, ok := p.instanceCache.Get(id); ok {
			return i.(*Instance), nil
		}
	}

	intID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid instance id %s, %w", id, err)
	}

	instance, err := p.client.GetInstance(ctx, intID)
	if linodego.IsNotFound(err) {
		p.instanceCache.Delete(id)
		return nil, cloudprovider.NewNodeClaimNotFoundError(err)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get linode instance, %w", err)
	}
	p.instanceCache.SetDefault(id, instance)

	return NewInstance(ctx, *instance), nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*Instance, error) {
	listFilter := utils.Filter{
		Tags: []string{
			karpv1.NodePoolLabelKey,
			apis.Group + "/linodenodeclass",
		},
	}
	filter, err := listFilter.String()
	if err != nil {
		return nil, err
	}
	instances, err := p.client.ListInstances(ctx, linodego.NewListOptions(1, filter))
	if err != nil {
		return nil, fmt.Errorf("failed to list linode instances, %w", err)
	}

	res := make([]*Instance, 0, len(instances))
	for _, it := range instances {
		p.instanceCache.SetDefault(strconv.Itoa(it.ID), it)
		res = append(res, NewInstance(ctx, it))
	}

	return res, cloudprovider.IgnoreNodeClaimNotFoundError(err)
}

func (p *DefaultProvider) Delete(ctx context.Context, id string) error {
	out, err := p.Get(ctx, id, SkipCache)
	if err != nil {
		return err
	}
	// Check if the instance is already shutting-down to reduce the number of
	// terminate-instance calls we make thereby reducing our overall QPS.
	if out.Status != linodego.InstanceDeleting {
		intID, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("invalid instance id %s, %w", id, err)
		}

		if err := p.client.DeleteInstance(ctx, intID); err != nil {
			return err
		}
	}
	return nil
}

// NOTE: Linode's API only supports creating tags one at a time. This might be a problem if we want to add multiple tags at once.
func (p *DefaultProvider) CreateTags(ctx context.Context, id string, tags map[string]string) error {
	intId, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid instance id %s, %w", id, err)
	}
	for k, v := range tags {
		if _, err := p.client.CreateTag(ctx, linodego.TagCreateOptions{
			Linodes: []int{intId},
			Label:   fmt.Sprintf("%s:%s", k, v),
		}); err != nil {
			if linodego.IsNotFound(err) {
				return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("tagging instance, %w", err))
			}
			return fmt.Errorf("tagging instance, %w", err)
		}
	}

	return nil
}
