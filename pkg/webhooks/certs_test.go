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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testDNSNames = []string{testService, testService + ".kube-system", testService + ".kube-system.svc"}

func TestEnsureCertsGeneratesAndReuses(t *testing.T) {
	t.Parallel()
	now := time.Now()

	bundle, changed, err := ensureCerts(nil, testDNSNames, now)
	require.NoError(t, err)
	require.True(t, changed)

	ca, caKey := loadCA(bundle, now)
	require.NotNil(t, ca)
	require.NotNil(t, caKey)
	require.True(t, leafIsValid(bundle, ca, testDNSNames, now))

	again, changed, err := ensureCerts(bundle.data(), testDNSNames, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, bundle.data(), again.data())
}

func TestEnsureCertsReissuesLeafOnly(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bundle, _, err := ensureCerts(nil, testDNSNames, now)
	require.NoError(t, err)

	t.Run("expiring leaf", func(t *testing.T) {
		t.Parallel()
		later := now.Add(leafValidity - leafRenewBefore + time.Hour)
		renewed, changed, err := ensureCerts(bundle.data(), testDNSNames, later)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, bundle.caBundle, renewed.caBundle)
		require.Equal(t, bundle.caKey, renewed.caKey)
		require.NotEqual(t, bundle.certPEM, renewed.certPEM)
		ca, _ := loadCA(renewed, later)
		require.True(t, leafIsValid(renewed, ca, testDNSNames, later))
	})

	t.Run("changed service name", func(t *testing.T) {
		t.Parallel()
		names := []string{"other", "other.kube-system"}
		renewed, changed, err := ensureCerts(bundle.data(), names, now)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, bundle.caBundle, renewed.caBundle)
		ca, _ := loadCA(renewed, now)
		require.True(t, leafIsValid(renewed, ca, names, now))
	})

	t.Run("corrupt serving key", func(t *testing.T) {
		t.Parallel()
		data := bundle.data()
		data[tlsKeyKey] = []byte("garbage")
		renewed, changed, err := ensureCerts(data, testDNSNames, now)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, bundle.caBundle, renewed.caBundle)
	})
}

func TestEnsureCertsRotatesCAKeepingPreviousTrusted(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bundle, _, err := ensureCerts(nil, testDNSNames, now)
	require.NoError(t, err)
	oldCAs, err := parseCertificates(bundle.caBundle)
	require.NoError(t, err)
	require.Len(t, oldCAs, 1)

	later := now.Add(caValidity - caRenewBefore + time.Hour)
	rotated, changed, err := ensureCerts(bundle.data(), testDNSNames, later)
	require.NoError(t, err)
	require.True(t, changed)

	cas, err := parseCertificates(rotated.caBundle)
	require.NoError(t, err)
	require.Len(t, cas, 2)
	require.NotEqual(t, oldCAs[0].Raw, cas[0].Raw)
	require.Equal(t, oldCAs[0].Raw, cas[1].Raw)

	ca, _ := loadCA(rotated, later)
	require.NotNil(t, ca)
	require.True(t, leafIsValid(rotated, ca, testDNSNames, later))
}

func TestEnsureCertsRegeneratesOnMismatchedCAKey(t *testing.T) {
	t.Parallel()
	now := time.Now()
	first, _, err := ensureCerts(nil, testDNSNames, now)
	require.NoError(t, err)
	second, _, err := ensureCerts(nil, testDNSNames, now)
	require.NoError(t, err)

	data := first.data()
	data[caKeyKey] = second.caKey
	rebuilt, changed, err := ensureCerts(data, testDNSNames, now)
	require.NoError(t, err)
	require.True(t, changed)
	ca, _ := loadCA(rebuilt, now)
	require.NotNil(t, ca)
	require.True(t, leafIsValid(rebuilt, ca, testDNSNames, now))
}
