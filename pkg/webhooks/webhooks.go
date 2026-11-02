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

// Package webhooks serves the CRD conversion webhook that converts LinodeNodeClass objects between API versions.
package webhooks

import (
	"crypto/tls"
	"fmt"
	"net/http"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/apis/v1beta1"
)

// ConversionPath is the path the apiserver calls to convert custom resources.
const ConversionPath = "/convert"

// NewScheme returns a scheme with every LinodeNodeClass version registered, along with its conversions.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, v1beta1.AddToScheme} {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// NewConversionHandler returns the handler converting LinodeNodeClass between its versions.
func NewConversionHandler() (http.Handler, error) {
	s, err := NewScheme()
	if err != nil {
		return nil, fmt.Errorf("building conversion scheme, %w", err)
	}
	return conversion.NewWebhookHandler(s, conversion.NewRegistry()), nil
}

// Register adds the conversion webhook server and its certificate manager to the manager.
func Register(mgr manager.Manager, clk clock.Clock, config Config) error {
	if err := config.validate(); err != nil {
		return fmt.Errorf("invalid webhook config, %w", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme.Scheme); err != nil {
		return fmt.Errorf("registering apiextensions types, %w", err)
	}
	handler, err := NewConversionHandler()
	if err != nil {
		return err
	}
	certs := NewCertManager(mgr.GetAPIReader(), mgr.GetClient(), clk, config)
	server := webhook.NewServer(webhook.Options{
		Port: config.Port,
		TLSOpts: []func(*tls.Config){func(c *tls.Config) {
			c.MinVersion = tls.VersionTLS12
			c.GetCertificate = certs.GetCertificate
		}},
	})
	server.Register(ConversionPath, handler)
	if err := mgr.Add(certs); err != nil {
		return fmt.Errorf("adding webhook certificate manager, %w", err)
	}
	if err := mgr.Add(server); err != nil {
		return fmt.Errorf("adding webhook server, %w", err)
	}
	return nil
}
