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

package v1alpha1

import (
	"fmt"

	apiconversion "k8s.io/apimachinery/pkg/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1beta1"
)

var _ conversion.Convertible = (*LinodeNodeClass)(nil)

// ConvertTo converts this v1alpha1 LinodeNodeClass to the v1beta1 hub version.
func (in *LinodeNodeClass) ConvertTo(raw conversion.Hub) error {
	dst, ok := raw.(*v1beta1.LinodeNodeClass)
	if !ok {
		return fmt.Errorf("unexpected conversion target %T, expected *v1beta1.LinodeNodeClass", raw)
	}
	return Convert_v1alpha1_LinodeNodeClass_To_v1beta1_LinodeNodeClass(in, dst, nil)
}

// ConvertFrom converts the v1beta1 hub version to this v1alpha1 LinodeNodeClass.
func (in *LinodeNodeClass) ConvertFrom(raw conversion.Hub) error {
	src, ok := raw.(*v1beta1.LinodeNodeClass)
	if !ok {
		return fmt.Errorf("unexpected conversion source %T, expected *v1beta1.LinodeNodeClass", raw)
	}
	return Convert_v1beta1_LinodeNodeClass_To_v1alpha1_LinodeNodeClass(src, in, nil)
}

func Convert_v1alpha1_LinodeNodeClassSpec_To_v1beta1_LinodeNodeClassSpec(in *LinodeNodeClassSpec, out *v1beta1.LinodeNodeClassSpec, s apiconversion.Scope) error {
	*out = v1beta1.LinodeNodeClassSpec{
		Tags: in.Tags,
	}
	if in.Kubelet != nil {
		out.Kubelet = new(v1beta1.KubeletConfiguration)
		if err := Convert_v1alpha1_KubeletConfiguration_To_v1beta1_KubeletConfiguration(in.Kubelet, out.Kubelet, s); err != nil {
			return err
		}
	}
	if in.LKEK8sVersion != nil {
		out.LKEMode = &v1beta1.LKEMode{LKEK8sVersion: in.LKEK8sVersion}
	}
	if !hasInstanceModeFields(in) {
		return nil
	}

	out.InstanceMode = &v1beta1.InstanceMode{
		AuthorizedKeys:  in.AuthorizedKeys,
		AuthorizedUsers: in.AuthorizedUsers,
		Image:           in.Image,
		BackupsEnabled:  in.BackupsEnabled,
		FirewallID:      in.FirewallID,
		DiskEncryption:  in.DiskEncryption,
		VPCID:           in.VPCID,
		SwapSize:        in.SwapSize,
	}
	if in.OSDisk != nil {
		out.InstanceMode.OSDisk = new(v1beta1.InstanceDisk)
		if err := Convert_v1alpha1_InstanceDisk_To_v1beta1_InstanceDisk(in.OSDisk, out.InstanceMode.OSDisk, s); err != nil {
			return err
		}
	}
	if in.DataDisks != nil {
		out.InstanceMode.DataDisks = new(v1beta1.InstanceDisks)
		if err := Convert_v1alpha1_InstanceDisks_To_v1beta1_InstanceDisks(in.DataDisks, out.InstanceMode.DataDisks, s); err != nil {
			return err
		}
	}
	if in.Configuration != nil {
		out.InstanceMode.Configuration = new(v1beta1.InstanceConfiguration)
		if err := Convert_v1alpha1_InstanceConfiguration_To_v1beta1_InstanceConfiguration(in.Configuration, out.InstanceMode.Configuration, s); err != nil {
			return err
		}
	}
	if in.PlacementGroup != nil {
		out.InstanceMode.PlacementGroup = new(v1beta1.InstanceCreatePlacementGroupOptions)
		if err := Convert_v1alpha1_InstanceCreatePlacementGroupOptions_To_v1beta1_InstanceCreatePlacementGroupOptions(in.PlacementGroup, out.InstanceMode.PlacementGroup, s); err != nil {
			return err
		}
	}
	if in.IPv6Options != nil {
		out.InstanceMode.IPv6Options = new(v1beta1.IPv6CreateOptions)
		if err := Convert_v1alpha1_IPv6CreateOptions_To_v1beta1_IPv6CreateOptions(in.IPv6Options, out.InstanceMode.IPv6Options, s); err != nil {
			return err
		}
	}
	return nil
}

func Convert_v1beta1_LinodeNodeClassSpec_To_v1alpha1_LinodeNodeClassSpec(in *v1beta1.LinodeNodeClassSpec, out *LinodeNodeClassSpec, s apiconversion.Scope) error {
	*out = LinodeNodeClassSpec{
		Tags: in.Tags,
	}
	if in.Kubelet != nil {
		out.Kubelet = new(KubeletConfiguration)
		if err := Convert_v1beta1_KubeletConfiguration_To_v1alpha1_KubeletConfiguration(in.Kubelet, out.Kubelet, s); err != nil {
			return err
		}
	}
	if in.LKEMode != nil {
		out.LKEK8sVersion = in.LKEMode.LKEK8sVersion
	}
	if in.InstanceMode == nil {
		return nil
	}

	out.AuthorizedKeys = in.InstanceMode.AuthorizedKeys
	out.AuthorizedUsers = in.InstanceMode.AuthorizedUsers
	out.Image = in.InstanceMode.Image
	out.BackupsEnabled = in.InstanceMode.BackupsEnabled
	out.FirewallID = in.InstanceMode.FirewallID
	out.DiskEncryption = in.InstanceMode.DiskEncryption
	out.VPCID = in.InstanceMode.VPCID
	out.SwapSize = in.InstanceMode.SwapSize

	if in.InstanceMode.OSDisk != nil {
		out.OSDisk = new(InstanceDisk)
		if err := Convert_v1beta1_InstanceDisk_To_v1alpha1_InstanceDisk(in.InstanceMode.OSDisk, out.OSDisk, s); err != nil {
			return err
		}
	}
	if in.InstanceMode.DataDisks != nil {
		out.DataDisks = new(InstanceDisks)
		if err := Convert_v1beta1_InstanceDisks_To_v1alpha1_InstanceDisks(in.InstanceMode.DataDisks, out.DataDisks, s); err != nil {
			return err
		}
	}
	if in.InstanceMode.Configuration != nil {
		out.Configuration = new(InstanceConfiguration)
		if err := Convert_v1beta1_InstanceConfiguration_To_v1alpha1_InstanceConfiguration(in.InstanceMode.Configuration, out.Configuration, s); err != nil {
			return err
		}
	}
	if in.InstanceMode.PlacementGroup != nil {
		out.PlacementGroup = new(InstanceCreatePlacementGroupOptions)
		if err := Convert_v1beta1_InstanceCreatePlacementGroupOptions_To_v1alpha1_InstanceCreatePlacementGroupOptions(in.InstanceMode.PlacementGroup, out.PlacementGroup, s); err != nil {
			return err
		}
	}
	if in.InstanceMode.IPv6Options != nil {
		out.IPv6Options = new(IPv6CreateOptions)
		if err := Convert_v1beta1_IPv6CreateOptions_To_v1alpha1_IPv6CreateOptions(in.InstanceMode.IPv6Options, out.IPv6Options, s); err != nil {
			return err
		}
	}
	return nil
}

func hasInstanceModeFields(spec *LinodeNodeClassSpec) bool {
	return spec.AuthorizedKeys != nil ||
		spec.AuthorizedUsers != nil ||
		spec.Image != "" ||
		spec.BackupsEnabled ||
		spec.FirewallID != nil ||
		spec.OSDisk != nil ||
		spec.DataDisks != nil ||
		spec.DiskEncryption != "" ||
		spec.Configuration != nil ||
		spec.PlacementGroup != nil ||
		spec.VPCID != nil ||
		spec.IPv6Options != nil ||
		spec.SwapSize != nil
}
