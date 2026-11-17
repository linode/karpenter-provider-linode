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
	"errors"
	"fmt"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

// CertDir is where the certificate Secret is mounted. The Helm chart mounts the Secret at this path.
const CertDir = "/etc/karpenter/webhook-certs"

// LinodeNodeClassCRDName is the CRD that serves multiple versions and therefore needs the conversion webhook.
var LinodeNodeClassCRDName = "linodenodeclasses." + apis.Group

// Config describes how the conversion webhook is exposed in the cluster.
type Config struct {
	// Namespace is the namespace of the Karpenter Service and certificate Secret.
	Namespace string
	// ServiceName is the Service fronting the webhook server. The certificate Secret is named <ServiceName>-cert.
	ServiceName string
	// Port is the port the webhook server listens on.
	Port int
}

func (c Config) secretName() string {
	return c.ServiceName + "-cert"
}

// dnsName is the name the apiserver uses to reach the webhook Service.
func (c Config) dnsName() string {
	return fmt.Sprintf("%s.%s.svc", c.ServiceName, c.Namespace)
}

func (c Config) extraDNSNames() []string {
	return []string{
		c.ServiceName,
		fmt.Sprintf("%s.%s", c.ServiceName, c.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", c.ServiceName, c.Namespace),
	}
}

func (c Config) validate() error {
	var errs []error
	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace is required"))
	}
	if c.ServiceName == "" {
		errs = append(errs, errors.New("service name is required"))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d is out of range", c.Port))
	}
	return errors.Join(errs...)
}
