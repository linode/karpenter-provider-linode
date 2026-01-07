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

package pricing

import (
	"context"
	"net/http"
	"sync"

	"github.com/linode/linodego"
)

type Provider interface {
	LivenessProbe(*http.Request) error
	InstanceTypes() []linodego.LinodeType
	// NOTE: I don't know if we're going to need the disambiguation of standard
	// vs dedicated pricing, so this is generic for now
	InstancePrice(linodego.LinodeType) (float64, bool)
	UpdatePricing(context.Context) error
}

// DefaultProvider provides actual pricing data to the Linode cloud provider to allow it to make more informed decisions
// regarding which instances to launch.  This is initialized at startup with a periodically updated static price list to
// support running in locations where pricing data is unavailable.  In those cases the static pricing data provides a
// relative ordering that is still more accurate than our previous pricing model.  In the event that a pricing update
// fails, the previous pricing information is retained and used which may be the static initial pricing data if pricing
// updates never succeed.
type DefaultProvider struct {
	// client sdk.LinodeAPI
	// region string
	// cm     *pretty.ChangeMonitor

	muInstance sync.RWMutex
	// instancePrices map[string]float64
}

// nolint: gocyclo
func (p *DefaultProvider) UpdatePricing(_ context.Context) error {
	// TODO: ????

	return nil
}

func (p *DefaultProvider) LivenessProbe(_ *http.Request) error {
	// ensure we don't deadlock and nolint for the empty critical section
	p.muInstance.Lock()
	//nolint: staticcheck
	p.muInstance.Unlock()
	return nil
}

func (p *DefaultProvider) Reset() {
	// TODO: ????
}
