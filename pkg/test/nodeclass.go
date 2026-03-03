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

package test

import (
	"fmt"

	"github.com/imdario/mergo"
	"sigs.k8s.io/karpenter/pkg/test"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
)

func LinodeNodeClass(overrides ...v1.LinodeNodeClass) *v1.LinodeNodeClass {
	options := v1.LinodeNodeClass{}
	for i := range overrides {
		if err := mergo.Merge(&options, overrides[i], mergo.WithOverride); err != nil {
			panic(fmt.Sprintf("Failed to merge settings: %s", err))
		}
	}
	return &v1.LinodeNodeClass{
		ObjectMeta: test.ObjectMeta(options.ObjectMeta),
		Spec:       options.Spec,
		Status:     options.Status,
	}
}

// LinodeNodeClassWithoutLKE creates a LinodeNodeClass configured for direct Linode instance tests.
// The mode (LKE vs instance) is controlled by the startup flag, not this nodeclass.
// This helper sets up appropriate defaults for instance mode testing (e.g., Image field).
func LinodeNodeClassWithoutLKE(overrides ...v1.LinodeNodeClass) *v1.LinodeNodeClass {
	nc := LinodeNodeClass(overrides...)
	// Set default image for instance mode if not provided
	if nc.Spec.Image == "" {
		nc.Spec.Image = "linode/ubuntu22.04"
	}
	return nc
}

type TestNodeClass struct {
	v1.LinodeNodeClass
}
