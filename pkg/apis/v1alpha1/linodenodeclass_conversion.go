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
