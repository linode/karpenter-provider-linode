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
	"reflect"
	"testing"

	"github.com/linode/linodego/v2"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1beta1"
)

func TestLinodeNodeClassSpecConversion(t *testing.T) {
	t.Parallel()

	input := &LinodeNodeClass{
		Spec: LinodeNodeClassSpec{
			AuthorizedKeys:  []string{"ssh-rsa AAAA"},
			AuthorizedUsers: []string{"admin"},
			Image:           "linode/ubuntu22.04",
			BackupsEnabled:  true,
			Tags:            []string{"env=production"},
			FirewallID:      new(123),
			OSDisk:          &InstanceDisk{Size: resource.MustParse("40Gi")},
			DataDisks:       &InstanceDisks{SDB: &InstanceDisk{Size: resource.MustParse("20Gi")}},
			DiskEncryption:  linodego.InstanceDiskEncryption("enabled"),
			Configuration:   &InstanceConfiguration{Kernel: "linode/latest-64bit"},
			PlacementGroup:  &InstanceCreatePlacementGroupOptions{ID: 1, CompliantOnly: new(true)},
			VPCID:           new(7),
			IPv6Options:     &IPv6CreateOptions{EnableSLAAC: new(true)},
			SwapSize:        new(512),
			Kubelet:         &KubeletConfiguration{},
			LKEK8sVersion:   new("v1.32.8+lke13"),
		},
	}
	want := v1beta1.LinodeNodeClassSpec{
		InstanceMode: &v1beta1.InstanceMode{
			AuthorizedKeys:  []string{"ssh-rsa AAAA"},
			AuthorizedUsers: []string{"admin"},
			Image:           "linode/ubuntu22.04",
			BackupsEnabled:  true,
			FirewallID:      new(123),
			OSDisk:          &v1beta1.InstanceDisk{Size: resource.MustParse("40Gi")},
			DataDisks:       &v1beta1.InstanceDisks{SDB: &v1beta1.InstanceDisk{Size: resource.MustParse("20Gi")}},
			DiskEncryption:  linodego.InstanceDiskEncryption("enabled"),
			Configuration:   &v1beta1.InstanceConfiguration{Kernel: "linode/latest-64bit"},
			PlacementGroup:  &v1beta1.InstanceCreatePlacementGroupOptions{ID: 1, CompliantOnly: new(true)},
			VPCID:           new(7),
			IPv6Options:     &v1beta1.IPv6CreateOptions{EnableSLAAC: new(true)},
			SwapSize:        new(512),
		},
		LKEMode: &v1beta1.LKEMode{LKEK8sVersion: new("v1.32.8+lke13")},
		Tags:    []string{"env=production"},
		Kubelet: &v1beta1.KubeletConfiguration{},
	}

	hub := &v1beta1.LinodeNodeClass{}
	if err := input.ConvertTo(hub); err != nil {
		t.Fatalf("converting to v1beta1: %v", err)
	}
	if !reflect.DeepEqual(hub.Spec, want) {
		t.Fatalf("converted spec mismatch:\n got: %#v\nwant: %#v", hub.Spec, want)
	}

	roundTripped := &LinodeNodeClass{}
	if err := roundTripped.ConvertFrom(hub); err != nil {
		t.Fatalf("converting from v1beta1: %v", err)
	}
	if !reflect.DeepEqual(roundTripped.Spec, input.Spec) {
		t.Fatalf("round-tripped spec mismatch:\n got: %#v\nwant: %#v", roundTripped.Spec, input.Spec)
	}
}

func TestLinodeNodeClassSpecConversionOmitsEmptyModes(t *testing.T) {
	t.Parallel()

	hub := &v1beta1.LinodeNodeClass{}
	if err := (&LinodeNodeClass{}).ConvertTo(hub); err != nil {
		t.Fatalf("converting empty spec to v1beta1: %v", err)
	}
	if hub.Spec.InstanceMode != nil {
		t.Fatalf("expected empty instance mode to be omitted, got %#v", hub.Spec.InstanceMode)
	}
	if hub.Spec.LKEMode != nil {
		t.Fatalf("expected empty LKE mode to be omitted, got %#v", hub.Spec.LKEMode)
	}
}
