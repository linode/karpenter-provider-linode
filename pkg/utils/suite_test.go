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
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/utils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUtils(t *testing.T) {
	t.Parallel()
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
	It("should return a map for key=value tags", func() {
		tagList := []string{"name=test-uid-123-5", "env=prod", "malformatedtag"}
		tagMap := utils.TagListToMap(tagList)
		Expect(tagMap).To(Equal(map[string]string{
			"name": "test-uid-123-5",
			"env":  "prod",
		}))
	})

	It("should skip colon-separated tags", func() {
		tagList := []string{"name:test-uid-123-5", "env=prod", "malformatedtag"}
		tagMap := utils.TagListToMap(tagList)
		Expect(tagMap).To(Equal(map[string]string{
			"env": "prod",
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

var _ = Describe("NormalizeNodeClaimTagValue", func() {
	It("should return name unchanged when shorter than max value length", func() {
		name := "short-name"
		result := utils.NormalizeNodeClaimTagValue(name)
		Expect(result).To(Equal(name))
	})

	It("should return name unchanged when exactly at max value length", func() {
		maxValueLength := 50 - len(v1.NodeClaimTagKey) - 1
		name := strings.Repeat("a", maxValueLength)

		result := utils.NormalizeNodeClaimTagValue(name)
		Expect(result).To(Equal(name))
		Expect(len(result)).To(Equal(maxValueLength))
	})

	It("should truncate long names with hash suffix", func() {
		longName := "very-long-nodeclaim-name-that-exceeds-the-maximum-allowed-length-for-linode-tags"
		result := utils.NormalizeNodeClaimTagValue(longName)

		Expect(result).To(ContainSubstring("-"))
		parts := strings.Split(result, "-")
		Expect(len(parts)).To(BeNumerically(">", 1))

		hashPart := parts[len(parts)-1]
		Expect(len(hashPart)).To(Equal(8))

		_, err := strconv.ParseUint(hashPart, 16, 32)
		Expect(err).To(BeNil())
	})

	It("should ensure output length stays within 50-character limit for NodeClaimTag", func() {
		longName := "extremely-long-nodeclaim-name-that-definitely-exceeds-any-reasonable-tag-length-limit"
		nodeClaimTag := utils.NodeClaimTag(longName)

		Expect(len(nodeClaimTag)).To(BeNumerically("<=", 50))
		Expect(nodeClaimTag).To(HavePrefix("karpenter.sh/nodeclaim="))
	})

	It("should handle edge case where prefix length becomes zero or negative", func() {
		veryLongName := "this-is-an-extremely-long-nodeclaim-name-that-would-cause-the-prefix-calculation-to-result-in-zero-or-negative-length"
		result := utils.NormalizeNodeClaimTagValue(veryLongName)

		Expect(result).ToNot(BeEmpty())
		maxValueLength := 50 - len(v1.NodeClaimTagKey) - 1
		Expect(len(result)).To(BeNumerically("<=", maxValueLength))
	})

	It("should produce consistent hash for the same input", func() {
		name := "consistent-test-name"
		result1 := utils.NormalizeNodeClaimTagValue(name)
		result2 := utils.NormalizeNodeClaimTagValue(name)
		Expect(result1).To(Equal(result2))
	})

	It("should produce different hashes for different inputs", func() {
		name1 := "test-name-one"
		name2 := "test-name-two"
		result1 := utils.NormalizeNodeClaimTagValue(name1)
		result2 := utils.NormalizeNodeClaimTagValue(name2)
		Expect(result1).ToNot(Equal(result2))
	})
})

var _ = Describe("NodeClaimTag", func() {
	It("should create properly formatted tag for short names", func() {
		name := "short-name"
		tag := utils.NodeClaimTag(name)
		Expect(tag).To(Equal("karpenter.sh/nodeclaim=short-name"))
	})

	It("should create properly formatted tag for truncated names", func() {
		longName := "very-long-nodeclaim-name-that-exceeds-the-maximum-allowed-length"
		tag := utils.NodeClaimTag(longName)

		Expect(tag).To(HavePrefix("karpenter.sh/nodeclaim="))

		parts := strings.Split(tag, "=")
		Expect(len(parts)).To(Equal(2))
		value := parts[1]

		expectedValue := utils.NormalizeNodeClaimTagValue(longName)
		Expect(value).To(Equal(expectedValue))
	})

	It("should ensure total tag length stays within Linode limits", func() {
		veryLongName := "this-is-an-extremely-long-nodeclaim-name-that-would-definitely-exceed-the-linode-tag-limit-if-not-properly-truncated"
		tag := utils.NodeClaimTag(veryLongName)

		Expect(len(tag)).To(BeNumerically("<=", 50))
	})
})
