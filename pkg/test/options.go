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
	"github.com/samber/lo"

	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
)

type OptionsFields struct {
	ClusterName             *string
	ClusterEndpoint         *string
	ClusterRegion           *string
	VMMemoryOverheadPercent *float64
}

func Options(overrides ...OptionsFields) *options.Options {
	opts := OptionsFields{}
	for _, override := range overrides {
		if err := mergo.Merge(&opts, override, mergo.WithOverride); err != nil {
			panic(fmt.Sprintf("Failed to merge settings: %s", err))
		}
	}
	return &options.Options{
		ClusterName:             lo.FromPtrOr(opts.ClusterName, "123456789012"),
		ClusterEndpoint:         lo.FromPtrOr(opts.ClusterEndpoint, "https://test-cluster"),
		ClusterRegion:           lo.FromPtrOr(opts.ClusterRegion, fake.DefaultRegion),
		VMMemoryOverheadPercent: lo.FromPtrOr(opts.VMMemoryOverheadPercent, 0.075),
	}
}
