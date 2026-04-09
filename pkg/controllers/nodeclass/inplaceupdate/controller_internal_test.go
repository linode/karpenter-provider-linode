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

package inplaceupdate

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/event"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
)

func TestNodeClassTagsChanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		oldTags  []string
		newTags  []string
		expected bool
	}{
		{
			name:     "returns true when tags change",
			oldTags:  []string{"owner=platform"},
			newTags:  []string{"team=infra"},
			expected: true,
		},
		{
			name:     "returns false when tags are semantically equal",
			oldTags:  []string{"owner=platform", "opaque-user-tag"},
			newTags:  []string{"opaque-user-tag", "owner=platform"},
			expected: false,
		},
		{
			name:     "returns false when both tag sets are empty",
			oldTags:  nil,
			newTags:  nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			oldNodeClass := &v1.LinodeNodeClass{Spec: v1.LinodeNodeClassSpec{Tags: tt.oldTags}}
			newNodeClass := &v1.LinodeNodeClass{Spec: v1.LinodeNodeClassSpec{Tags: tt.newTags}}

			got := nodeClassTagsChanged(event.UpdateEvent{
				ObjectOld: oldNodeClass,
				ObjectNew: newNodeClass,
			})
			if got != tt.expected {
				t.Fatalf("expected %t, got %t", tt.expected, got)
			}
		})
	}
}
