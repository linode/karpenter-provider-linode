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
	"github.com/linode/linodego"
)

func InstanceID() int {
	return randomdata.Number(1000000, 9999999)
}

func RandomProviderID() string {
	return ProviderID(InstanceID())
}

func ProviderID(id int) string {
	return fmt.Sprintf("linode://%d", id)
}

func MakeInstances() []linodego.LinodeType {
	return defaultLinodeTypeList
}

func MakeInstanceOfferings(types []linodego.LinodeType) []linodego.RegionAvailability {
	var res []linodego.RegionAvailability
	for _, t := range types {
		res = append(res, linodego.RegionAvailability{
			Region:    DefaultRegion,
			Plan:      t.ID,
			Available: true,
		})
	}
	return res
}
