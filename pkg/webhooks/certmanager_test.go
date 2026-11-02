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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const testService = "karpenter"

var testConfig = Config{Namespace: "kube-system", ServiceName: testService, Port: 8443}

func newFakeClient(t *testing.T, funcs *interceptor.Funcs) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(*funcs).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: testConfig.Namespace, Name: testConfig.secretName()}},
		&apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: LinodeNodeClassCRDName}},
	).Build()
}

func TestCertManagerReconcile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	updates := 0
	c := newFakeClient(t, &interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		updates++
		return c.Update(ctx, obj, opts...)
	}})
	m := NewCertManager(c, c, clocktesting.NewFakeClock(time.Now()), testConfig)

	_, err := m.GetCertificate(nil)
	require.Error(t, err, "no certificate should be served before the first reconcile")

	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 2, updates, "expected one Secret update and one CRD update")

	secret := &corev1.Secret{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: testConfig.Namespace, Name: "karpenter-cert"}, secret))
	for _, key := range []string{caCertKey, caKeyKey, tlsCertKey, tlsKeyKey} {
		require.NotEmpty(t, secret.Data[key], key)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: LinodeNodeClassCRDName}, crd))
	require.NotNil(t, crd.Spec.Conversion)
	require.Equal(t, apiextensionsv1.WebhookConverter, crd.Spec.Conversion.Strategy)
	cc := crd.Spec.Conversion.Webhook.ClientConfig
	require.Equal(t, secret.Data[caCertKey], cc.CABundle)
	require.Equal(t, testService, cc.Service.Name)
	require.Equal(t, "kube-system", cc.Service.Namespace)
	require.Equal(t, ConversionPath, *cc.Service.Path)
	require.EqualValues(t, 8443, *cc.Service.Port)

	cert, err := m.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)

	require.NoError(t, m.Reconcile(ctx))
	require.Equal(t, 2, updates, "a second reconcile must not write again")
}

func TestCertManagerReplicasConverge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newFakeClient(t, &interceptor.Funcs{})
	clk := clocktesting.NewFakeClock(time.Now())
	first := NewCertManager(c, c, clk, testConfig)
	second := NewCertManager(c, c, clk, testConfig)

	require.NoError(t, first.Reconcile(ctx))
	require.NoError(t, second.Reconcile(ctx))

	a, err := first.GetCertificate(nil)
	require.NoError(t, err)
	b, err := second.GetCertificate(nil)
	require.NoError(t, err)
	require.Equal(t, a.Certificate, b.Certificate)
}

func TestCertManagerMissingSecret(t *testing.T) {
	t.Parallel()
	c := newFakeClient(t, &interceptor.Funcs{})
	m := NewCertManager(c, c, clocktesting.NewFakeClock(time.Now()), Config{Namespace: "other", ServiceName: testService, Port: 8443})
	require.Error(t, m.Reconcile(context.Background()))
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, testConfig.validate())
	require.Error(t, Config{ServiceName: testService, Port: 8443}.validate())
	require.Error(t, Config{Namespace: "kube-system", Port: 8443}.validate())
	require.Error(t, Config{Namespace: "kube-system", ServiceName: testService}.validate())
}
