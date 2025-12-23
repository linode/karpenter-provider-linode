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
	"context"
	"sync"
	"time"

	"github.com/linode/linodego"
	"k8s.io/utils/ptr"
)

var (
	// TODO: get this list from a static file
	defaultLinodeTypeList = []linodego.LinodeType{
		{
			ID:       "g6-standard-2",
			Label:    "Linode 4GB",
			Memory:   4096,
			VCPUs:    2,
			Disk:     81920,
			Transfer: 1000,
			Price:    &linodego.LinodePrice{Monthly: 20.0, Hourly: 0.03},
		},
	}
	defaultRegionAvailabilityList = []linodego.RegionAvailability{
		{
			Region:    DefaultRegion,
			Plan:      "g6-standard-2",
			Available: true,
		},
		{
			Region:    DefaultRegion,
			Plan:      "g6-standard-4",
			Available: true,
		},
		{
			Region:    DefaultRegion,
			Plan:      "g6-dedicated-4",
			Available: false,
		},
	}
)

type LinodeAPIBehavior struct {
	LinodeTypeList                  AtomicPtr[[]linodego.LinodeType]
	RegionAvailabilityList          AtomicPtr[[]linodego.RegionAvailability]
	GetTypeBehavior                 MockedFunction[string, *linodego.LinodeType]
	ListRegionsAvailabilityBehavior MockedFunction[linodego.ListOptions, []linodego.RegionAvailability]
	CreateInstanceBehavior          MockedFunction[linodego.InstanceCreateOptions, *linodego.Instance]
	GetInstanceBehavior             MockedFunction[int, *linodego.Instance]
	DeleteInstanceBehavior          MockedFunction[int, error]
	ListInstancesBehavior           MockedFunction[linodego.ListOptions, []linodego.Instance]
	CreateTagsBehavior              MockedFunction[linodego.TagCreateOptions, linodego.Tag]
	ListTypesBehavior               MockedFunction[linodego.ListOptions, []linodego.LinodeType]
	NextError                       AtomicError
	Instances                       sync.Map
}

type LinodeClient struct {
	LinodeAPIBehavior
}

func (l *LinodeClient) GetType(_ context.Context, typeID string) (*linodego.LinodeType, error) {
	linodeType, err := l.GetTypeBehavior.Invoke(&typeID, func(typeID *string) (**linodego.LinodeType, error) {
		// TODO: return the other fields as well when support is added to map types to resources
		return ptr.To(&linodego.LinodeType{
			ID: *typeID,
			Price: &linodego.LinodePrice{
				Monthly: 20.0,
				Hourly:  0.03,
			},
		}), nil
	})
	if linodeType == nil {
		return nil, err
	}
	return *linodeType, err
}

func (l *LinodeClient) ListRegionsAvailability(_ context.Context, _ *linodego.ListOptions) ([]linodego.RegionAvailability, error) {
	if !l.NextError.IsNil() {
		defer l.NextError.Reset()
		return nil, l.NextError.Get()
	}
	if !l.LinodeTypeList.IsNil() {
		return *l.RegionAvailabilityList.Clone(), nil
	}
	return defaultRegionAvailabilityList, nil
}

func (l *LinodeClient) GetInstance(_ context.Context, linodeID int) (*linodego.Instance, error) {
	instance, err := l.GetInstanceBehavior.Invoke(&linodeID, func(linodeID *int) (**linodego.Instance, error) {
		return ptr.To(&linodego.Instance{
			ID:      *linodeID,
			Image:   "linode/ubuntu22.04",
			Label:   "example-instance",
			Type:    "g6-standard-2",
			Region:  DefaultRegion,
			Created: ptr.To(time.Now().Add(-1 * time.Hour)),
			Status:  linodego.InstanceRunning,
		}), nil
	})
	if instance == nil {
		return nil, err
	}
	return *instance, err
}

func NewLinodeClient() *LinodeClient {
	return &LinodeClient{}
}

func (l *LinodeClient) Reset() {
	l.LinodeTypeList.Reset()
	l.DeleteInstanceBehavior.Reset()
	l.ListInstancesBehavior.Reset()
	l.Instances.Range(func(k, v any) bool {
		l.Instances.Delete(k)
		return true
	})
}

func (l *LinodeClient) CreateInstance(_ context.Context, opts linodego.InstanceCreateOptions) (*linodego.Instance, error) {
	instance, err := l.CreateInstanceBehavior.Invoke(&opts, func(opts *linodego.InstanceCreateOptions) (**linodego.Instance, error) {
		return ptr.To(&linodego.Instance{
			ID:                  len(opts.Label) + 1, // just a simple way to generate an ID
			Image:               opts.Image,
			Label:               opts.Label,
			Type:                opts.Type,
			Region:              opts.Region,
			InterfaceGeneration: opts.InterfaceGeneration,
			Created:             ptr.To(time.Now()),
			Status:              linodego.InstanceRunning,
		}), nil
	})
	if instance == nil {
		return nil, err
	}
	return *instance, err
}

func (l *LinodeClient) ListInstances(_ context.Context, opts *linodego.ListOptions) ([]linodego.Instance, error) {
	instances, err := l.ListInstancesBehavior.Invoke(opts, func(opts *linodego.ListOptions) (*[]linodego.Instance, error) {
		var instances []linodego.Instance
		l.Instances.Range(func(k interface{}, v interface{}) bool {
			instances = append(instances, v.(linodego.Instance))
			return true
		})
		return &instances, nil
	})
	if instances == nil {
		return nil, err
	}
	return *instances, err
}

func (l *LinodeClient) DeleteInstance(_ context.Context, linodeID int) error {
	_, err := l.DeleteInstanceBehavior.Invoke(&linodeID, func(linodeID *int) (*error, error) {
		l.Instances.LoadAndDelete(linodeID)
		return nil, nil
	})
	return err
}

func (l *LinodeClient) CreateTag(_ context.Context, opts linodego.TagCreateOptions) (*linodego.Tag, error) {
	tag, err := l.CreateTagsBehavior.Invoke(&opts, func(opts *linodego.TagCreateOptions) (*linodego.Tag, error) {
		return &linodego.Tag{
			Label: opts.Label,
		}, nil
	})
	return tag, err
}

func (l *LinodeClient) ListTypes(_ context.Context, _ *linodego.ListOptions) ([]linodego.LinodeType, error) {
	if !l.NextError.IsNil() {
		defer l.NextError.Reset()
		return nil, l.NextError.Get()
	}
	if !l.LinodeTypeList.IsNil() {
		return *l.LinodeTypeList.Clone(), nil
	}
	return defaultLinodeTypeList, nil
}
