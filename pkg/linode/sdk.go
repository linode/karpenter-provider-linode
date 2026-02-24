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

package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/linode/linodego"
)

type LinodeAPI interface {
	ListTypes(ctx context.Context, opts *linodego.ListOptions) ([]linodego.LinodeType, error)
	GetType(ctx context.Context, typeID string) (*linodego.LinodeType, error)
	// ListRegionsAvailability returns availability of plan types AND the prices for each, so we shouldn't need a PricingAPI interface
	ListRegionsAvailability(ctx context.Context, opts *linodego.ListOptions) ([]linodego.RegionAvailability, error)
	GetInstance(ctx context.Context, linodeID int) (*linodego.Instance, error)
	DeleteInstance(ctx context.Context, linodeID int) error
	ListInstances(ctx context.Context, opts *linodego.ListOptions) ([]linodego.Instance, error)
	CreateTag(ctx context.Context, opts linodego.TagCreateOptions) (*linodego.Tag, error)
	CreateInstance(ctx context.Context, opts linodego.InstanceCreateOptions) (*linodego.Instance, error)
	UpdateInstance(ctx context.Context, linodeID int, opts linodego.InstanceUpdateOptions) (*linodego.Instance, error)

	// LKE Cluster methods for LKE cluster management
	ListLKEClusters(ctx context.Context, opts *linodego.ListOptions) ([]linodego.LKECluster, error)
	UpdateLKEClusterControlPlaneACL(ctx context.Context, clusterID int, opts linodego.LKEClusterControlPlaneACLUpdateOptions) (*linodego.LKEClusterControlPlaneACLResponse, error)
	GetLKEClusterControlPlaneACL(ctx context.Context, clusterID int) (*linodego.LKEClusterControlPlaneACLResponse, error)

	// NodePool methods for LKE cluster management
	CreateLKENodePool(ctx context.Context, clusterID int, opts linodego.LKENodePoolCreateOptions) (*linodego.LKENodePool, error)
	ListLKENodePools(ctx context.Context, clusterID int, opts *linodego.ListOptions) ([]linodego.LKENodePool, error)
	GetLKENodePool(ctx context.Context, clusterID, poolID int) (*linodego.LKENodePool, error)
	UpdateLKENodePool(ctx context.Context, clusterID, poolID int, opts linodego.LKENodePoolUpdateOptions) (*linodego.LKENodePool, error)
	DeleteLKENodePool(ctx context.Context, clusterID, poolID int) error
	DeleteLKENodePoolNode(ctx context.Context, clusterID int, nodeID string) error
}

const (
	defaultClientTimeout = time.Second * 10
)

type Option struct {
	set func(client *linodego.Client)
}

type ClientConfig struct {
	Token   string
	Timeout time.Duration
}

func CreateLinodeClient(config ClientConfig, opts ...Option) (LinodeAPI, error) {
	if config.Token == "" {
		return nil, errors.New("token cannot be empty")
	}

	timeout := defaultClientTimeout
	if config.Timeout != 0 {
		timeout = config.Timeout
	}

	// Use system cert pool if root CA cert was not provided explicitly for this client.
	// Works around linodego not using system certs if LINODE_CA is provided,
	// which affects all clients spawned via linodego.NewClient
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	newClient := linodego.NewClient(httpClient)
	newClient.SetToken(config.Token)
	newClient.SetUserAgent("KARPL")

	for _, opt := range opts {
		opt.set(&newClient)
	}

	return &newClient, nil
}
