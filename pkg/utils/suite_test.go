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

package utils_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Utils Suite")
}

var _ = Describe("GetNodeClassHash", func() {
	It("should return formatted hash with UID and Generation", func() {
		nodeClass := &v1.LinodeNodeClass{
			ObjectMeta: metav1.ObjectMeta{
				UID:        "test-uid-123",
				Generation: 5,
			},
		}
		hash := utils.GetNodeClassHash(nodeClass)
		Expect(hash).To(Equal("test-uid-123-5"))
	})
})

var _ = Describe("TagListToMap", func() {
	It("should return a map for key:pair tags", func() {
		tagList := []string{"name:test-uid-123-5", "env:prod", "malformatedtag"}
		tagMap := utils.TagListToMap(tagList)
		Expect(tagMap).To(Equal(map[string]string{
			"name": "test-uid-123-5",
			"env":  "prod",
		}))
	})
})

var _ = Describe("DedupeStrings", func() {
	It("should remove duplicates and empty entries while preserving order", func() {
		input := []string{"a", "b", "a", "", "c", "b"}
		Expect(utils.DedupeTags(input)).To(Equal([]string{"a", "b", "c"}))
	})
})

var _ = Describe("ParseInstanceID", func() {
	It("should parse valid instance IDs", func() {
		providerID := "linode://123456"
		instanceID, err := utils.ParseInstanceID(providerID)
		Expect(err).To(BeNil())
		Expect(instanceID).To(Equal("123456"))
	})
	It("should return error for no provider name", func() {
		instanceID := "123456"
		_, err := utils.ParseInstanceID(instanceID)
		Expect(err).ToNot(BeNil())
	})
})
