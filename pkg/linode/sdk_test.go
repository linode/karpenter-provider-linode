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

package sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	. "github.com/linode/karpenter-provider-linode/pkg/linode"
)

// Test_createLinodeClient tests the createLinodeClient function. Checks if the client does not error out.
func TestCreateLinodeClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		token               string
		baseUrl             string
		rootCertificatePath string
		timeout             time.Duration
		expectedErr         error
	}{
		{
			"Success - Valid API token",
			"test-key",
			"",
			"",
			0,
			nil,
		},
		{
			"Error - Empty API token",
			"",
			"",
			"",
			0,
			errors.New("token cannot be empty"),
		},
	}

	for _, tt := range tests {
		testCase := tt
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clientConfig := ClientConfig{
				Token:   testCase.token,
				Timeout: testCase.timeout,
			}
			got, err := CreateLinodeClient(clientConfig)
			if testCase.expectedErr != nil {
				assert.EqualError(t, err, testCase.expectedErr.Error())
			} else {
				assert.NotNil(t, got)
			}
		})
	}
}
