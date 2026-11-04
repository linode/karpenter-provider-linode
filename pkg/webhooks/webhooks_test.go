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

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

func convert(t *testing.T, h http.Handler, desiredVersion string, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	review := apiextensionsv1.ConversionReview{
		TypeMeta: metaV1ConversionReview(),
		Request: &apiextensionsv1.ConversionRequest{
			UID:               types.UID("uid"),
			DesiredAPIVersion: apis.Group + "/" + desiredVersion,
			Objects:           []runtime.RawExtension{{Raw: raw}},
		},
	}
	body, err := json.Marshal(review)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, ConversionPath, bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code)

	out := apiextensionsv1.ConversionReview{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.Response)
	require.Equal(t, "Success", out.Response.Result.Status, out.Response.Result.Message)
	require.Len(t, out.Response.ConvertedObjects, 1)
	converted := map[string]any{}
	require.NoError(t, json.Unmarshal(out.Response.ConvertedObjects[0].Raw, &converted))
	return converted
}

func nodeClass(version string) map[string]any {
	spec := map[string]any{
		"tags": []any{"tag"},
		"kubelet": map[string]any{
			"maxPods":        float64(110),
			"systemReserved": map[string]any{"cpu": "100m"},
		},
	}
	instanceFields := map[string]any{
		"image":          "linode/ubuntu24.04",
		"backupsEnabled": true,
		"firewallID":     float64(1234),
	}
	if version == "v1alpha1" {
		for k, v := range instanceFields {
			spec[k] = v
		}
	} else {
		spec["instanceMode"] = instanceFields
	}
	return map[string]any{
		"apiVersion": apis.Group + "/" + version,
		"kind":       "LinodeNodeClass",
		"metadata":   map[string]any{"name": "default", "labels": map[string]any{"a": "b"}},
		"spec":       spec,
		"status": map[string]any{
			"linodeImages": []any{map[string]any{"id": "linode/ubuntu24.04", "label": "Ubuntu 24.04"}},
		},
	}
}

func TestConversionHandlerConvertsBetweenVersions(t *testing.T) {
	t.Parallel()
	h, err := NewConversionHandler()
	require.NoError(t, err)

	up := convert(t, h, "v1beta1", nodeClass("v1alpha1"))
	require.Equal(t, nodeClass("v1beta1"), up)

	down := convert(t, h, "v1alpha1", nodeClass("v1beta1"))
	require.Equal(t, nodeClass("v1alpha1"), down)
}

func TestConversionHandlerRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	h, err := NewConversionHandler()
	require.NoError(t, err)

	raw, err := json.Marshal(nodeClass("v1alpha1"))
	require.NoError(t, err)
	body, err := json.Marshal(apiextensionsv1.ConversionReview{
		TypeMeta: metaV1ConversionReview(),
		Request: &apiextensionsv1.ConversionRequest{
			UID:               "uid",
			DesiredAPIVersion: apis.Group + "/v1gamma1",
			Objects:           []runtime.RawExtension{{Raw: raw}},
		},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, ConversionPath, bytes.NewReader(body)))

	out := apiextensionsv1.ConversionReview{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "Failure", out.Response.Result.Status)
}

func metaV1ConversionReview() metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: "apiextensions.k8s.io/v1", Kind: "ConversionReview"}
}
