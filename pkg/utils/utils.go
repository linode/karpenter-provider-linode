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

package utils

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/awslabs/operatorpkg/serrors"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

var (
	instanceIDRegex = regexp.MustCompile(`(?P<Provider>.*):///(?P<AZ>.*)/(?P<InstanceID>.*)`)
)

// ParseInstanceID parses the provider ID stored on the node to get the instance ID
// associated with a node
func ParseInstanceID(providerID string) (string, error) {
	matches := instanceIDRegex.FindStringSubmatch(providerID)
	if matches == nil {
		return "", serrors.Wrap(fmt.Errorf("provider id does not match known format"), "provider-id", providerID)
	}
	for i, name := range instanceIDRegex.SubexpNames() {
		if name == "InstanceID" {
			return matches[i], nil
		}
	}
	return "", serrors.Wrap(fmt.Errorf("provider id does not match known format"), "provider-id", providerID)
}

// PrettySlice truncates a slice after a certain number of max items to ensure
// that the Slice isn't too long
func PrettySlice[T any](s []T, maxItems int) string {
	var sb strings.Builder
	for i, elem := range s {
		if i > maxItems-1 {
			fmt.Fprintf(&sb, " and %d other(s)", len(s)-i)
			break
		} else if i > 0 {
			fmt.Fprint(&sb, ", ")
		}
		fmt.Fprint(&sb, elem)
	}
	return sb.String()
}

// WithDefaultFloat64 returns the float64 value of the supplied environment variable or, if not present,
// the supplied default value. If the float64 conversion fails, returns the default
func WithDefaultFloat64(key string, def float64) float64 {
	val, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return def
	}
	return f
}

func GetTags(nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, clusterName string) (map[string]string, error) {
	return nil, nil
}

func GetNodeClassHash(nodeClass *v1.LinodeNodeClass) string {
	return fmt.Sprintf("%s-%d", nodeClass.UID, nodeClass.Generation)
}

// Filter holds the fields used for filtering results from the Linode API.
//
// The fields within Filter are prioritized so that only the most-specific
// field is present when Filter is marshaled to JSON.
type Filter struct {
	ID                *int              // Filter on the resource's ID (most specific).
	Label             string            // Filter on the resource's label.
	Tags              []string          // Filter resources by their tags (least specific).
	AdditionalFilters map[string]string // Filter resources by additional parameters
}

// MarshalJSON returns a JSON-encoded representation of a [Filter].
// The resulting encoded value will have exactly 1 (one) field present.
// See [Filter] for details on the value precedence.
func (f Filter) MarshalJSON() ([]byte, error) {
	filter := make(map[string]string, len(f.AdditionalFilters)+1)
	switch {
	case f.ID != nil:
		filter["id"] = strconv.Itoa(*f.ID)
	case f.Label != "":
		filter["label"] = f.Label
	case len(f.Tags) != 0:
		filter["tags"] = strings.Join(f.Tags, ",")
	}

	maps.Copy(filter, f.AdditionalFilters)
	return json.Marshal(filter)
}

// String returns the string representation of the encoded value from
// [Filter.MarshalJSON].
func (f Filter) String() (string, error) {
	p, err := f.MarshalJSON()
	if err != nil {
		return "", err
	}

	return string(p), nil
}
