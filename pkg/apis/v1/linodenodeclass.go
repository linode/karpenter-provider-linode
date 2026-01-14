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

package v1

import (
	"fmt"

	"github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LinodeNodeClassSpec is the top level specification for the Linode Karpenter Provider.
// This will contain configuration necessary to launch instances in Linode.
type LinodeNodeClassSpec struct {
	// authorizedKeys is a list of SSH public keys to add to the instance.
	// +optional
	// +listType=set
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`

	// authorizedUsers is a list of usernames to add to the instance.
	// +optional
	// +listType=set
	AuthorizedUsers []string `json:"authorizedUsers,omitempty"`

	// image is the Linode image to use for the instance.
	// +optional
	Image string `json:"image,omitempty"`

	// linodeInterfaces is a list of Linode network interfaces to use for the instance. Requires Linode Interfaces beta opt-in to use.
	// +optional
	// +kubebuilder:object:generate=true
	// +listType=atomic
	LinodeInterfaces []LinodeInterfaceCreateOptions `json:"linodeInterfaces,omitempty"`

	// backupsEnabled is a boolean indicating whether backups should be enabled for the instance.
	// +optional
	BackupsEnabled bool `json:"backupsEnabled,omitempty"`

	// tags is a list of tags to apply to the Linode instance.
	// +optional
	// +listType=set
	Tags []string `json:"tags,omitempty"`

	// labels is a map of Kubernetes node labels to apply to the Node.
	// +optional
	Labels linodego.LKENodePoolLabels `json:"labels,omitempty"`

	// firewallID is the id of the cloud firewall to apply to the Linode Instance
	// +optional
	FirewallID int `json:"firewallID,omitempty"`

	// osDisk is a configuration for the root disk that includes the OS,
	// if not specified, this defaults to whatever space is not taken up by the DataDisks
	// +optional
	OSDisk *InstanceDisk `json:"osDisk,omitempty"`

	// dataDisks is a map of any additional disks to add to an instance,
	// The sum of these disks + the OSDisk must not be more than allowed on a linodes plan
	// +optional
	DataDisks *InstanceDisks `json:"dataDisks,omitempty"`

	// diskEncryption determines if the disks of the instance should be encrypted. The default is disabled.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	DiskEncryption linodego.InstanceDiskEncryption `json:"diskEncryption,omitempty"`

	// configuration is the Akamai instance configuration OS,
	// if not specified, this defaults to the default configuration associated to the instance.
	// +optional
	Configuration *InstanceConfiguration `json:"configuration,omitempty"`

	// placementGroup makes the linode to be launched in that specific group.
	// +optional
	PlacementGroup *InstanceCreatePlacementGroupOptions `json:"placementGroup,omitempty"`

	// vpcID is the ID of an existing VPC in Linode.
	// +optional
	VPCID *int `json:"vpcID,omitempty"`

	// ipv6Options defines the IPv6 options for the instance.
	// If not specified, IPv6 ranges won't be allocated to instance.
	// +optional
	IPv6Options *IPv6CreateOptions `json:"ipv6Options,omitempty"`

	// swapSize is the size of the swap disk in MiB.
	SwapSize *int `json:"swap_size,omitempty"`

	// Kubelet defines args to be used when configuring kubelet on provisioned nodes.
	// They are a subset of the upstream types, recognizing not all options may be supported.
	// Wherever possible, the types and names should reflect the upstream kubelet types.
	// +kubebuilder:validation:XValidation:message="evictionSoft OwnerKey does not have a matching evictionSoftGracePeriod",rule="has(self.evictionSoft) ? self.evictionSoft.all(e, (e in self.evictionSoftGracePeriod)):true"
	// +kubebuilder:validation:XValidation:message="evictionSoftGracePeriod OwnerKey does not have a matching evictionSoft",rule="has(self.evictionSoftGracePeriod) ? self.evictionSoftGracePeriod.all(e, (e in self.evictionSoft)):true"
	// +optional
	Kubelet *KubeletConfiguration `json:"kubelet,omitempty"`

	// managedLKE enables LKE-managed node pool mode. When true (default), Karpenter provisions
	// nodes via LKE NodePool APIs instead of creating direct Linode instances.
	// In this mode, LKE manages the node image, network configuration, and kubelet.
	// Fields like image, authorizedKeys, linodeInterfaces, osDisk, dataDisks,
	// placementGroup, configuration, vpcID, ipv6Options, swapSize, and kubelet are ignored.
	// Set to false to use direct Linode instance provisioning.
	// +optional
	// +kubebuilder:default=true
	ManagedLKE *bool `json:"managedLKE,omitempty"`

	// lkeK8sVersion is the Kubernetes version for LKE node pools.
	// Only used when managedLKE is true. Only available for Enterprise LKE clusters.
	// +optional
	LKEK8sVersion *string `json:"lkeK8sVersion,omitempty"`

	// lkeUpdateStrategy defines how nodes are updated when the LKE node pool is modified.
	// Only used when managedLKE is true.
	// Valid values are "rolling_update" and "on_recycle".
	// +optional
	// +kubebuilder:validation:Enum=rolling_update;on_recycle
	LKEUpdateStrategy *linodego.LKENodePoolUpdateStrategy `json:"lkeUpdateStrategy,omitempty"`
}

type InstanceCreatePlacementGroupOptions struct {
	// ID is the Linode Placement Group ID to launch the instance in.
	// +required
	ID int `json:"id"`
	// CompliantOnly indicates whether all Linodes in this group should be compliant with the group's placement group type
	// +optional
	CompliantOnly *bool `json:"compliant_only,omitempty"`
}

// IPv6CreateOptions defines the IPv6 options for the instance.
type IPv6CreateOptions struct {

	// enableSLAAC is an option to enable SLAAC (Stateless Address Autoconfiguration) for the instance.
	// This is useful for IPv6 addresses, allowing the instance to automatically configure its own IPv6 address.
	// Defaults to false.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	EnableSLAAC *bool `json:"enableSLAAC,omitempty"`

	// enableRanges is an option to enable IPv6 ranges for the instance.
	// If set to true, the instance will have a range of IPv6 addresses.
	// This is useful for instances that require multiple IPv6 addresses.
	// Defaults to false.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	EnableRanges *bool `json:"enableRanges,omitempty"`

	// isPublicIPv6 is an option to enable public IPv6 for the instance.
	// If set to true, the instance will have a publicly routable IPv6 range.
	// Defaults to false.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	IsPublicIPv6 *bool `json:"isPublicIPv6,omitempty"`
}

type InstanceDisks struct {
	// sdb is a disk for the instance.
	// +optional
	SDB *InstanceDisk `json:"sdb,omitempty"`
	// sdc is a disk for the instance.
	// +optional
	SDC *InstanceDisk `json:"sdc,omitempty"`
	// sdd is a disk for the instance.
	// +optional
	SDD *InstanceDisk `json:"sdd,omitempty"`
	// sde is a disk for the instance.
	// +optional
	SDE *InstanceDisk `json:"sde,omitempty"`
	// sdf is a disk for the instance.
	// +optional
	SDF *InstanceDisk `json:"sdf,omitempty"`
	// sdg is a disk for the instance.
	// +optional
	SDG *InstanceDisk `json:"sdg,omitempty"`
	// sdh is a disk for the instance.
	// +optional
	SDH *InstanceDisk `json:"sdh,omitempty"`
}

// InstanceDisk defines a list of disks to use for an instance
type InstanceDisk struct {
	// diskID is the linode assigned ID of the disk.
	// +optional
	DiskID int `json:"diskID,omitempty"`

	// size of the disk in resource.Quantity notation.
	// +required
	Size resource.Quantity `json:"size,omitempty"`

	// label for the instance disk, if nothing is provided, it will match the device name.
	// +optional
	Label string `json:"label,omitempty"`

	// filesystem of disk to provision, the default disk filesystem is "ext4".
	// +optional
	// +kubebuilder:validation:Enum=raw;swap;ext3;ext4;initrd
	Filesystem string `json:"filesystem,omitempty"`
}

// InstanceMetadataOptions defines metadata of instance
type InstanceMetadataOptions struct {
	// userData expects a Base64-encoded string.
	// +optional
	UserData string `json:"userData,omitempty"`
}

// InstanceConfiguration defines the instance configuration
type InstanceConfiguration struct {
	// kernel is a Kernel ID to boot a Linode with. (e.g linode/latest-64bit).
	// +optional
	Kernel string `json:"kernel,omitempty"`
}

// InstanceConfigInterfaceCreateOptions defines network interface config
type InstanceConfigInterfaceCreateOptions struct {
	// ipamAddress is the IP address to assign to the interface.
	// +optional
	IPAMAddress string `json:"ipamAddress,omitempty"`

	// label is the label of the interface.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Label string `json:"label,omitempty"`

	// purpose is the purpose of the interface.
	// +optional
	Purpose linodego.ConfigInterfacePurpose `json:"purpose,omitempty"`

	// primary is a boolean indicating whether the interface is primary.
	// +optional
	Primary bool `json:"primary,omitempty"`

	// subnetId is the ID of the subnet to use for the interface.
	// +optional
	SubnetID *int `json:"subnetId,omitempty"`

	// ipv4 is the IPv4 configuration for the interface.
	// +optional
	IPv4 *VPCIPv4 `json:"ipv4,omitempty"`

	// ipRanges is a list of IPv4 ranges to assign to the interface.
	// +optional
	// +listType=set
	IPRanges []string `json:"ipRanges,omitempty"`
}

// LinodeInterfaceCreateOptions defines the linode network interface config
type LinodeInterfaceCreateOptions struct {
	// firewallID is the ID of the firewall to use for the interface.
	// +optional
	FirewallID *int `json:"firewallID,omitempty"`

	// defaultRoute is the default route for the interface.
	// +optional
	DefaultRoute *InterfaceDefaultRoute `json:"defaultRoute,omitempty"`

	// public is the public interface configuration for the interface.
	// +optional
	Public *PublicInterfaceCreateOptions `json:"public,omitempty"`

	// vpc is the VPC interface configuration for the interface.
	// +optional
	VPC *VPCInterfaceCreateOptions `json:"vpc,omitempty"`

	// vlan is the VLAN interface configuration for the interface.
	// +optional
	VLAN *VLANInterface `json:"vlan,omitempty"`
}

// InterfaceDefaultRoute defines the default IPv4 and IPv6 routes for an interface
type InterfaceDefaultRoute struct {
	// ipv4 is the IPv4 default route for the interface.
	// +optional
	IPv4 *bool `json:"ipv4,omitempty"`

	// ipv6 is the IPv6 default route for the interface.
	// +optional
	IPv6 *bool `json:"ipv6,omitempty"`
}

// PublicInterfaceCreateOptions defines the IPv4 and IPv6 public interface create options
type PublicInterfaceCreateOptions struct {
	// ipv4 is the IPv4 configuration for the public interface.
	// +optional
	IPv4 *PublicInterfaceIPv4CreateOptions `json:"ipv4,omitempty"`

	// ipv6 is the IPv6 configuration for the public interface.
	// +optional
	IPv6 *PublicInterfaceIPv6CreateOptions `json:"ipv6,omitempty"`
}

// PublicInterfaceIPv4CreateOptions defines the PublicInterfaceIPv4AddressCreateOptions for addresses
type PublicInterfaceIPv4CreateOptions struct {
	// addresses is the IPv4 addresses for the public interface.
	// +optional
	// +listType=map
	// +listMapKey=address
	Addresses []PublicInterfaceIPv4AddressCreateOptions `json:"addresses,omitempty"`
}

// PublicInterfaceIPv4AddressCreateOptions defines the public IPv4 address and whether it is primary
type PublicInterfaceIPv4AddressCreateOptions struct {
	// address is the IPv4 address for the public interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Address string `json:"address,omitempty"`

	// primary is a boolean indicating whether the address is primary.
	// +optional
	Primary *bool `json:"primary,omitempty"`
}

// PublicInterfaceIPv6CreateOptions defines the PublicInterfaceIPv6RangeCreateOptions
type PublicInterfaceIPv6CreateOptions struct {
	// ranges is the IPv6 ranges for the public interface.
	// +optional
	// +listType=map
	// +listMapKey=range
	Ranges []PublicInterfaceIPv6RangeCreateOptions `json:"ranges,omitempty"`
}

// PublicInterfaceIPv6RangeCreateOptions defines the IPv6 range for a public interface
type PublicInterfaceIPv6RangeCreateOptions struct {
	// range is the IPv6 range for the public interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Range string `json:"range,omitempty"`
}

// VPCInterfaceCreateOptions defines the VPC interface configuration for an instance
type VPCInterfaceCreateOptions struct {
	// subnetId is the ID of the subnet to use for the interface.
	// +optional
	SubnetID *int `json:"subnetId,omitempty"`

	// ipv4 is the IPv4 configuration for the interface.
	// +optional
	IPv4 *VPCInterfaceIPv4CreateOptions `json:"ipv4,omitempty"`

	// ipv6 is the IPv6 configuration for the interface.
	// +optional
	IPv6 *VPCInterfaceIPv6CreateOptions `json:"ipv6,omitempty"`
}

// VPCInterfaceIPv6CreateOptions defines the IPv6 configuration for a VPC interface
type VPCInterfaceIPv6CreateOptions struct {
	// slaac is the IPv6 SLAAC configuration for the interface.
	// +optional
	// +listType=map
	// +listMapKey=range
	SLAAC []VPCInterfaceIPv6SLAACCreateOptions `json:"slaac,omitempty"`

	// ranges is the IPv6 ranges for the interface.
	// +optional
	// +listType=map
	// +listMapKey=range
	Ranges []VPCInterfaceIPv6RangeCreateOptions `json:"ranges,omitempty"`

	// isPublic is a boolean indicating whether the interface is public.
	// +required
	IsPublic *bool `json:"isPublic,omitempty"`
}

// VPCInterfaceIPv6SLAACCreateOptions defines the Range for IPv6 SLAAC
type VPCInterfaceIPv6SLAACCreateOptions struct {
	// range is the IPv6 range for the interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Range string `json:"range,omitempty"`
}

// VPCInterfaceIPv6RangeCreateOptions defines the IPv6 range for a VPC interface
type VPCInterfaceIPv6RangeCreateOptions struct {
	// range is the IPv6 range for the interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Range string `json:"range,omitempty"`
}

// VPCInterfaceIPv4CreateOptions defines the IPv4 address and range configuration for a VPC interface
type VPCInterfaceIPv4CreateOptions struct {
	// addresses is the IPv4 addresses for the interface.
	// +optional
	// +listType=map
	// +listMapKey=address
	Addresses []VPCInterfaceIPv4AddressCreateOptions `json:"addresses,omitempty"`

	// ranges is the IPv4 ranges for the interface.
	// +optional
	// +listType=map
	// +listMapKey=range
	Ranges []VPCInterfaceIPv4RangeCreateOptions `json:"ranges,omitempty"`
}

// VPCInterfaceIPv4AddressCreateOptions defines the IPv4 configuration for a VPC interface
type VPCInterfaceIPv4AddressCreateOptions struct {
	// address is the IPv4 address for the interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Address string `json:"address,omitempty"`

	// primary is a boolean indicating whether the address is primary.
	// +optional
	Primary *bool `json:"primary,omitempty"`

	// nat1to1Address is the NAT 1:1 address for the interface.
	// +optional
	NAT1To1Address *string `json:"nat1to1Address,omitempty"`
}

// VPCInterfaceIPv4RangeCreateOptions defines the IPv4 range for a VPC interface
type VPCInterfaceIPv4RangeCreateOptions struct {
	// range is the IPv4 range for the interface.
	// +kubebuilder:validation:MinLength=1
	// +required
	Range string `json:"range,omitempty"`
}

// VLANInterface defines the VLAN interface configuration for an instance
type VLANInterface struct {
	// vlanLabel is the label of the VLAN.
	// +kubebuilder:validation:MinLength=1
	// +required
	VLANLabel string `json:"vlanLabel,omitempty"`

	// ipamAddress is the IP address to assign to the interface.
	// +optional
	IPAMAddress *string `json:"ipamAddress,omitempty"`
}

// VPCIPv4 defines VPC IPV4 settings
type VPCIPv4 struct {
	// vpc is the ID of the VPC to use for the interface.
	// +optional
	VPC string `json:"vpc,omitempty"`

	// nat1to1 is the NAT 1:1 address for the interface.
	// +optional
	NAT1To1 string `json:"nat1to1,omitempty"`
}

// KubeletConfiguration defines args to be used when configuring kubelet on provisioned nodes.
// They are a subset of the upstream types, recognizing not all options may be supported.
// Wherever possible, the types and names should reflect the upstream kubelet types.
// https://pkg.go.dev/k8s.io/kubelet/config/v1beta1#KubeletConfiguration
// https://github.com/kubernetes/kubernetes/blob/9f82d81e55cafdedab619ea25cabf5d42736dacf/cmd/kubelet/app/options/options.go#L53
type KubeletConfiguration struct {
	// clusterDNS is a list of IP addresses for the cluster DNS server.
	// Note that not all providers may use all addresses.
	//+optional
	ClusterDNS []string `json:"clusterDNS,omitempty"`
	// MaxPods is an override for the maximum number of pods that can run on
	// a worker node instance.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`
	// PodsPerCore is an override for the number of pods that can run on a worker node
	// instance based on the number of cpu cores. This value cannot exceed MaxPods, so, if
	// MaxPods is a lower value, that value will be used.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	PodsPerCore *int32 `json:"podsPerCore,omitempty"`
	// SystemReserved contains resources reserved for OS system daemons and kernel memory.
	// +kubebuilder:validation:XValidation:message="valid keys for systemReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="systemReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	SystemReserved map[string]string `json:"systemReserved,omitempty"`
	// KubeReserved contains resources reserved for Kubernetes system components.
	// +kubebuilder:validation:XValidation:message="valid keys for kubeReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="kubeReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	KubeReserved map[string]string `json:"kubeReserved,omitempty"`
	// EvictionHard is the map of signal names to quantities that define hard eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionHard are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionHard map[string]string `json:"evictionHard,omitempty"`
	// EvictionSoft is the map of signal names to quantities that define soft eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoft are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionSoft map[string]string `json:"evictionSoft,omitempty"`
	// EvictionSoftGracePeriod is the map of signal names to quantities that define grace periods for each eviction signal
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoftGracePeriod are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionSoftGracePeriod map[string]metav1.Duration `json:"evictionSoftGracePeriod,omitempty"`
	// EvictionMaxPodGracePeriod is the maximum allowed grace period (in seconds) to use when terminating pods in
	// response to soft eviction thresholds being met.
	// +optional
	EvictionMaxPodGracePeriod *int32 `json:"evictionMaxPodGracePeriod,omitempty"`
	// CPUCFSQuota enables CPU CFS quota enforcement for containers that specify CPU limits.
	// +optional
	CPUCFSQuota *bool `json:"cpuCFSQuota,omitempty"`
}

// LinodeNodeClass is the Schema for the LinodeNodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:printcolumn:name="Role",type="string",JSONPath=".spec.role",priority=1,description=""
// +kubebuilder:resource:path=linodenodeclasses,scope=Cluster,categories=karpenter,shortName={lnc,lncs}
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
type LinodeNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LinodeNodeClassSpec   `json:"spec,omitempty"`
	Status LinodeNodeClassStatus `json:"status,omitempty"`
}

// We need to bump the LinodeNodeClassHashVersion when we make an update to the LinodeNodeClassHashVersion CRD under these conditions:
// 1. A field changes its default value for an existing field that is already hashed
// 2. A field is added to the hash calculation with an already-set value
// 3. A field is removed from the hash calculations
const LinodeNodeClassHashVersion = "v1"

func (in *LinodeNodeClass) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash([]interface{}{
		in.Spec,
	}, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

func (in *LinodeNodeClass) KubeletConfiguration() *KubeletConfiguration {
	return in.Spec.Kubelet
}

// IsLKEManaged returns true if this NodeClass is configured for LKE-managed mode.
// Defaults to true if not explicitly set.
func (in *LinodeNodeClass) IsLKEManaged() bool {
	return in.Spec.ManagedLKE == nil || *in.Spec.ManagedLKE
}

func (in *LinodeNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

func (in *LinodeNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}

func (in *LinodeNodeClass) StatusConditions() status.ConditionSet {
	conds := []string{}
	return status.NewReadyConditions(conds...).For(in)
}

// +kubebuilder:object:root=true

// LinodeNodeClassList contains a list of LinodeNodeClass
type LinodeNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinodeNodeClass `json:"items"`
}
