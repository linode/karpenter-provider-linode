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

package fake

import (
	"fmt"

	"github.com/Pallinder/go-randomdata"
)

func InstanceID() string {
	return fmt.Sprintf("i-%s", randomdata.Alphanumeric(17))
}

func RandomProviderID() string {
	return ProviderID(InstanceID())
}

func ProviderID(id string) string {
	return fmt.Sprintf("linode://%s", id)
}

func PrivateDNSName() string {
	return fmt.Sprintf("ip-192-168-%d-%d.%s.compute.internal", randomdata.Number(0, 256), randomdata.Number(0, 256), DefaultRegion)
}
