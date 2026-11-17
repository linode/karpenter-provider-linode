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

package lke_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linode/linodego/v2"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

func TestLKECreateIsolation(t *testing.T) {
	t.Parallel()
	for _, tier := range []linodego.LKEVersionTier{linodego.LKEVersionEnterprise, linodego.LKEVersionStandard} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			client := &isolatedClaimClient{LinodeClient: fake.NewLinodeClient()}
			client.ClusterTier = tier
			pool := &linodego.LKENodePool{
				ID: 10, Type: standardNodeType, Count: 1, Tags: enterprisePoolTags("pool-a"),
				Linodes: []linodego.LKENodePoolLinode{{InstanceID: 100}},
			}
			client.NodePools.Store(fmt.Sprintf("%d-10", fake.DefaultClusterID), pool)
			valid := linodego.Instance{
				ID: 100, Type: standardNodeType, LKEClusterID: fake.DefaultClusterID,
				Tags: enterpriseInstanceTags("pool-a", 10), Created: new(time.Now()),
			}
			client.Instances.Store(valid.ID, valid)
			// These sort ahead of the eligible worker. Each violates a different
			// discovery predicate or an existing client-side eligibility check.
			for i, tags := range [][]string{
				enterpriseInstanceTags("pool-b", 20),
				{v1.LabelLKEManaged + "=true", "nodepool=10", "lke999"},
				{"nodepool=10", fmt.Sprintf("lke%d", fake.DefaultClusterID)},
				{v1.LabelLKEManaged + "=false", "nodepool=10", fmt.Sprintf("lke%d", fake.DefaultClusterID)},
				append(enterpriseInstanceTags("pool-a", 10), utils.NodeClaimTag("already-owned")),
			} {
				decoy := valid
				decoy.ID, decoy.Tags = i+1, tags
				if i == 1 {
					decoy.LKEClusterID = 999
				}
				client.Instances.Store(decoy.ID, decoy)
			}
			wrongType := valid
			wrongType.ID, wrongType.Type = 6, standard8GBType
			client.Instances.Store(wrongType.ID, wrongType)

			provider := newIsolationProvider(client, tier)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for range 2 { // A retry must recover the same worker without another ownership write.
				node, err := createIsolatedClaim(ctx, provider, "claim-a", "pool-a")
				require.NoError(t, err)
				require.Equal(t, valid.ID, node.ID)
			}
			require.Equal(t, 1, client.UpdateInstanceBehavior.Calls())
			for id := 1; id <= 6; id++ {
				decoy, err := client.GetInstance(ctx, id)
				require.NoError(t, err)
				require.NotContains(t, decoy.Tags, utils.NodeClaimTag("claim-a"))
			}
		})
	}
}

func TestLKEConcurrentClaimIsolation(t *testing.T) {
	t.Parallel()
	client := &isolatedClaimClient{
		LinodeClient: fake.NewLinodeClient(),
		snapshots:    make(chan struct{}, 2), releaseDiscovery: make(chan struct{}), firstVerified: make(chan struct{}),
	}
	client.ClusterTier = linodego.LKEVersionEnterprise
	for i, name := range []string{"pool-a", "pool-b"} {
		poolID, instanceID := (i+1)*10, (i+1)*100
		client.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), &linodego.LKENodePool{
			ID: poolID, Type: standardNodeType, Count: 1, Tags: enterprisePoolTags(name),
		})
		client.Instances.Store(instanceID, linodego.Instance{
			ID: instanceID, Type: standardNodeType, LKEClusterID: fake.DefaultClusterID,
			Tags: enterpriseInstanceTags(name, poolID), Created: new(time.Now()),
		})
	}
	provider := newIsolationProvider(client, linodego.LKEVersionEnterprise)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		name string
		node *instance.Instance
		err  error
	}
	results := make(chan result, 2)
	for _, name := range []string{"a", "b"} {
		go func() {
			node, err := createIsolatedClaim(ctx, provider, "claim-"+name, "pool-"+name)
			results <- result{name: name, node: node, err: err}
		}()
	}
	// Both API list snapshots must be captured before either claim writes tags.
	for range 2 {
		select {
		case <-client.snapshots:
		case <-ctx.Done():
			t.Fatal("concurrent discovery did not reach both pool locks")
		}
	}
	close(client.releaseDiscovery)
	returned := map[string]*instance.Instance{}
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			returned[result.name] = result.node
		case <-ctx.Done():
			t.Fatal("concurrent claims did not complete")
		}
	}
	t.Logf("concurrent claims returned VM IDs: a=%d b=%d", returned["a"].ID, returned["b"].ID)
	require.Equal(t, 100, returned["a"].ID)
	require.Equal(t, 200, returned["b"].ID)
	for i, name := range []string{"a", "b"} {
		worker, err := client.GetInstance(ctx, (i+1)*100)
		require.NoError(t, err)
		require.Contains(t, worker.Tags, utils.NodeClaimTag("claim-"+name))
		require.Contains(t, worker.Tags, "karpenter.sh/nodepool=pool-"+name)
		// Retry through the public entrypoint after both writes have completed.
		node, err := createIsolatedClaim(ctx, provider, "claim-"+name, "pool-"+name)
		require.NoError(t, err)
		require.Equal(t, worker.ID, node.ID)
	}
	require.Equal(t, 2, client.UpdateInstanceBehavior.Calls())
}

func TestLKEListIsolation(t *testing.T) {
	t.Parallel()
	for _, tier := range []linodego.LKEVersionTier{linodego.LKEVersionEnterprise, linodego.LKEVersionStandard} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			client := fake.NewLinodeClient()
			for i, tags := range [][]string{
				enterpriseInstanceTags("pool-a", 10),
				{fmt.Sprintf("lke%d", fake.DefaultClusterID)}, // Unmanaged seed worker.
				{v1.LabelLKEManaged + "=false", fmt.Sprintf("lke%d", fake.DefaultClusterID)},
				{v1.LabelLKEManaged + "=true", "lke999"},
			} {
				worker := linodego.Instance{ID: i + 1, Type: standardNodeType, Tags: tags, LKEClusterID: fake.DefaultClusterID}
				if i == 3 {
					worker.LKEClusterID = 999
				}
				client.Instances.Store(worker.ID, worker)
			}
			nodes, err := newIsolationProvider(client, tier).List(context.Background())
			require.NoError(t, err)
			ids := make([]int, 0, len(nodes))
			for _, node := range nodes {
				ids = append(ids, node.ID)
			}
			require.Equal(t, []int{1}, ids)
		})
	}
}

func newIsolationProvider(client sdk.LinodeAPI, tier linodego.LKEVersionTier) *lke.DefaultProvider {
	return lke.NewDefaultProvider(
		fake.DefaultClusterID, tier, fake.DefaultClusterName, fake.DefaultRegion,
		nil, client, nil, cache.New(time.Minute, 0),
		lke.ProviderConfig{CreateDeadline: time.Second, RetryDelay: time.Millisecond},
	)
}

func createIsolatedClaim(ctx context.Context, provider *lke.DefaultProvider, name, pool string) (*instance.Instance, error) {
	claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{karpv1.NodePoolLabelKey: pool}}}
	instanceTypes := []*cloudprovider.InstanceType{{
		Name: standardNodeType, Requirements: scheduling.NewRequirements(), Overhead: &cloudprovider.InstanceTypeOverhead{},
		Offerings: cloudprovider.Offerings{{Available: true, Price: 1, Requirements: scheduling.NewRequirements()}},
	}}
	return provider.Create(ctx, &v1.LinodeNodeClass{}, claim, nil, instanceTypes)
}

// isolatedClaimClient preserves the fake's actual filter evaluation. Stable API
// ordering makes the old broad filter choose the same VM in both pool locks.
// Channels force A to verify before B writes, exposing two successful Creates
// rather than merely making one contender time out on ownership verification.
type isolatedClaimClient struct {
	*fake.LinodeClient
	snapshots        chan struct{}
	releaseDiscovery chan struct{}
	firstVerified    chan struct{}
	verifyOnce       sync.Once
}

func (c *isolatedClaimClient) ListInstances(ctx context.Context, opts *linodego.ListOptions) ([]linodego.Instance, error) {
	workers, err := c.LinodeClient.ListInstances(ctx, opts)
	slices.SortFunc(workers, func(a, b linodego.Instance) int { return a.ID - b.ID })
	if err == nil && c.snapshots != nil && !strings.Contains(opts.Filter, v1.NodeClaimTagKey) {
		select {
		case c.snapshots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-c.releaseDiscovery:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return workers, err
}

func (c *isolatedClaimClient) UpdateInstance(ctx context.Context, id int, opts linodego.InstanceUpdateOptions) (*linodego.Instance, error) {
	if c.firstVerified != nil && slices.Contains(opts.Tags, utils.NodeClaimTag("claim-b")) {
		select {
		case <-c.firstVerified:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.LinodeClient.UpdateInstance(ctx, id, opts)
}

func (c *isolatedClaimClient) GetInstance(ctx context.Context, id int) (*linodego.Instance, error) {
	worker, err := c.LinodeClient.GetInstance(ctx, id)
	if err == nil && c.firstVerified != nil && slices.Contains(worker.Tags, utils.NodeClaimTag("claim-a")) {
		c.verifyOnce.Do(func() { close(c.firstVerified) })
	}
	return worker, err
}
