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

package v1beta1

import (
	"testing"

	"k8s.io/utils/ptr"
)

func TestHashEmptyModesMatchAbsent(t *testing.T) {
	t.Parallel()

	absent := (&LinodeNodeClass{}).Hash()
	tests := map[string]LinodeNodeClassSpec{
		"empty instanceMode":            {InstanceMode: &InstanceMode{}},
		"empty lkeMode":                 {LKEMode: &LKEMode{}},
		"both empty":                    {InstanceMode: &InstanceMode{}, LKEMode: &LKEMode{}},
		"empty authorizedKeys":          {InstanceMode: &InstanceMode{AuthorizedKeys: []string{}}},
		"tags only":                     {Tags: []string{"a"}},
		"tags with empty modes present": {Tags: []string{"a"}, LKEMode: &LKEMode{}},
	}
	for name, spec := range tests {
		if got := (&LinodeNodeClass{Spec: spec}).Hash(); got != absent {
			t.Errorf("%s: hash %s differs from absent hash %s", name, got, absent)
		}
	}
}

func TestHashNonEmptyModesDiffer(t *testing.T) {
	t.Parallel()

	absent := (&LinodeNodeClass{}).Hash()
	tests := map[string]LinodeNodeClassSpec{
		"instanceMode keys": {InstanceMode: &InstanceMode{AuthorizedKeys: []string{"k"}}},
		"lkeMode version":   {LKEMode: &LKEMode{LKEK8sVersion: ptr.To("v1.36")}},
	}
	for name, spec := range tests {
		if got := (&LinodeNodeClass{Spec: spec}).Hash(); got == absent {
			t.Errorf("%s: hash unexpectedly equals absent hash", name)
		}
	}
}

func TestHashDoesNotMutateSpec(t *testing.T) {
	t.Parallel()

	nc := &LinodeNodeClass{Spec: LinodeNodeClassSpec{LKEMode: &LKEMode{}}}
	_ = nc.Hash()
	if nc.Spec.LKEMode == nil {
		t.Fatal("Hash must not mutate the spec")
	}
}
