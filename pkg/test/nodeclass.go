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

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

func LinodeNodeClass(overrides ...v1.LinodeNodeClass) *v1.LinodeNodeClass {
	options := v1.LinodeNodeClass{}
	for _, override := range overrides {
		if err := mergo.Merge(&options, override, mergo.WithOverride); err != nil {
			panic(fmt.Sprintf("Failed to merge settings: %s", err))
		}
	}
	return &v1.LinodeNodeClass{
		ObjectMeta: test.ObjectMeta(options.ObjectMeta),
		Spec:       options.Spec,
		Status:     options.Status,
	}
}

type TestNodeClass struct {
	v1.LinodeNodeClass
}
