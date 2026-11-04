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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: apis.Group, Version: "v1alpha1"}

	// SchemeBuilder collects the functions that register this package's types and conversions.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// localSchemeBuilder is referenced by the conversion-gen output.
	localSchemeBuilder = &SchemeBuilder
	// AddToScheme registers this package's types and conversions in a scheme.
	AddToScheme = localSchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	metav1.AddToGroupVersion(s, GroupVersion)
	s.AddKnownTypes(GroupVersion,
		&LinodeNodeClass{},
		&LinodeNodeClassList{},
	)
	return nil
}

func init() {
	if err := AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
}
