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

package instance

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	b64 "encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/option"
	"github.com/google/uuid"
	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

var SkipCache = func(opts *cacheopts) {
	opts.SkipCache = true
}
var defaultImage = "linode/ubuntu22.04"

type Provider interface {
	Create(context.Context, *v1alpha1.LinodeNodeClass, *karpv1.NodeClaim, map[string]string, []*cloudprovider.InstanceType) (*Instance, error)
	Get(context.Context, string, ...Options) (*Instance, error)
	List(context.Context) ([]*Instance, error)
	Delete(context.Context, string) error
	CreateTags(context.Context, string, map[string]string) error
}

type cacheopts struct {
	SkipCache bool
}

type Options = option.Function[cacheopts]

type DefaultProvider struct {
	region               string
	recorder             events.Recorder
	linodeAPI            sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	instanceCache        *cache.Cache
	kubeClient           kubernetes.Interface
	clusterID            int
}

func NewDefaultProvider(
	region string,
	recorder events.Recorder,
	linodeAPI sdk.LinodeAPI,
	unavailableOfferings *linodecache.UnavailableOfferings,
	instanceCache *cache.Cache,
	kubeClient kubernetes.Interface,
	clusterID int,
) *DefaultProvider {
	return &DefaultProvider{
		region:               region,
		recorder:             recorder,
		linodeAPI:            linodeAPI,
		unavailableOfferings: unavailableOfferings,
		instanceCache:        instanceCache,
		kubeClient:           kubeClient,
		clusterID:            clusterID,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*Instance, error) {
	instanceTypes, err := utils.FilterInstanceTypes(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, err
	}
	cheapestType, err := utils.CheapestInstanceType(instanceTypes)
	if err != nil {
		return nil, err
	}
	// Merge tags from NodeClaim and LinodeNodeClass
	tagList := nodeClass.Spec.Tags
	for k, v := range tags {
		tagList = append(tagList, fmt.Sprintf("%s=%s", k, v))
	}

	if nodeClass.Spec.Image == "" {
		nodeClass.Spec.Image = defaultImage
	}
	createOpts := linodego.InstanceCreateOptions{
		Label:           fmt.Sprintf("%s-%s", nodeClaim.Name, uuid.NewString()[0:8]),
		Region:          p.region,
		Type:            cheapestType.Name,
		RootPass:        uuid.NewString(),
		AuthorizedKeys:  nodeClass.Spec.AuthorizedKeys,
		AuthorizedUsers: nodeClass.Spec.AuthorizedUsers,
		Image:           nodeClass.Spec.Image,
		BackupsEnabled:  nodeClass.Spec.BackupsEnabled,
		Tags:            utils.DedupeTags(tagList),
		// NOTE: Disk encryption may not currently be available to all users.
		DiskEncryption: nodeClass.Spec.DiskEncryption,
		SwapSize:       nodeClass.Spec.SwapSize,
	}

	if options.FromContext(ctx).Mode != options.ProvisionModeLKE {
		userData, err := p.buildUserData(ctx, nodeClass)
		if err != nil {
			return nil, err
		}
		createOpts.Metadata = &linodego.InstanceMetadataOptions{UserData: userData}
	}

	if nodeClass.Spec.FirewallID != nil {
		createOpts.FirewallID = *nodeClass.Spec.FirewallID
	}

	if nodeClass.Spec.PlacementGroup != nil {
		createOpts.PlacementGroup = &linodego.InstanceCreatePlacementGroupOptions{
			ID:            nodeClass.Spec.PlacementGroup.ID,
			CompliantOnly: nodeClass.Spec.PlacementGroup.CompliantOnly,
		}
	}

	instance, err := p.linodeAPI.CreateInstance(ctx, createOpts)
	// Update the offerings cache based on the error returned from the CreateInstance call.
	utils.UpdateUnavailableOfferingsCache(
		ctx,
		err,
		p.region,
		cheapestType,
		p.unavailableOfferings,
	)
	if err != nil {
		return nil, cloudprovider.NewCreateError(err, "InstanceCreationFailed", fmt.Sprintf("Failed to create Linode instance: %s", err.Error()))
	}

	// If in instance mode, need to add the new instance IP into the control plane ACL so it can connect at cloud-init time when it runs kubeadm join
	if options.FromContext(ctx).Mode != options.ProvisionModeLKE {
		if err := p.updateACL(ctx, instance); err != nil {
			return nil, err
		}
	}

	return NewInstance(ctx, instance), nil
}

func (p *DefaultProvider) updateACL(ctx context.Context, instance *linodego.Instance) error {
	// First get the current ACL to update since we can't just add the IPs, we need the original list
	controlPlaneACL, err := p.linodeAPI.GetLKEClusterControlPlaneACL(ctx, p.clusterID)
	if err != nil {
		return err
	}
	// Then get provisioned instance addresses
	ipv4s := make([]string, len(instance.IPv4))
	for i, ipv4 := range instance.IPv4 {
		ipv4s[i] = ipv4.String()
	}
	// Then combine them and update the ACL
	if controlPlaneACL != nil && controlPlaneACL.ACL.Addresses != nil {
		if _, err := p.linodeAPI.UpdateLKEClusterControlPlaneACL(ctx, p.clusterID, linodego.LKEClusterControlPlaneACLUpdateOptions{
			ACL: linodego.LKEClusterControlPlaneACLOptions{
				Enabled: ptr.To(controlPlaneACL.ACL.Enabled),
				Addresses: &linodego.LKEClusterControlPlaneACLAddressesOptions{
					IPv4: ptr.To(append(controlPlaneACL.ACL.Addresses.IPv4, ipv4s...)),
					IPv6: ptr.To(append(controlPlaneACL.ACL.Addresses.IPv6, instance.IPv6)),
				},
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *DefaultProvider) buildUserData(ctx context.Context, _ *v1alpha1.LinodeNodeClass) (string, error) {
	// First figure out the cluster endpoint via the cluster-info configmap and the CA crt hash
	clusterInfoConfigMap, err := p.kubeClient.CoreV1().ConfigMaps("kube-public").Get(ctx, "cluster-info", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get cluster-info configmap: %w", err)
	}
	p.kubeClient.Discovery().ServerVersion()
	// Parse the kubeconfig stored in the ConfigMap
	kubeconfig, err := clientcmd.Load([]byte(clusterInfoConfigMap.Data["kubeconfig"]))
	if err != nil {
		return "", fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Get the server URL from the first cluster
	var clusterEndpoint string
	var caCertHash string
	for _, cluster := range kubeconfig.Clusters {
		clusterEndpoint = strings.TrimPrefix(cluster.Server, "https://") // e.g. "<hash>.<region>-gw.linodelke.net:443"
		// Decode the PEM-encoded CA cert
		block, _ := pem.Decode(cluster.CertificateAuthorityData)
		if block == nil {
			return "", fmt.Errorf("failed to decode CA cert PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("failed to parse CA cert: %w", err)
		}
		// Hash the public key (DER-encoded SubjectPublicKeyInfo)
		pubKeyDER, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
		if err != nil {
			return "", fmt.Errorf("failed to marshal public key: %w", err)
		}
		hash := sha256.Sum256(pubKeyDER)
		caCertHash = hex.EncodeToString(hash[:])
		break
	}

	// Generate a bootstrap token
	tokenID, tokenSecret, err := createToken(ctx, p.kubeClient, time.Hour)
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	// Construct the kubeadm join command for cloud-init. The actual command looks like:
	// kubeadm join <control-plane-host>:<control-plane-port> --token <token> --discovery-token-ca-cert-hash sha256:<hash>
	k8sVersion, err := p.kubeClient.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("failed to generate k8s server version: %w", err)
	}
	bootstrapData := fmt.Sprintf(`#cloud-config
runcmd:
  - curl -fsSL https://github.com/linode/cluster-api-provider-linode/raw/dd76b1f979696ef22ce093d420cdbd0051a1d725/scripts/pre-kubeadminit.sh | bash -s %s
  - hostname "{{ ds.meta_data.label }}"
  - echo "{{ ds.meta_data.label }}" >/etc/hostname
  - mkdir -p /etc/kubernetes/manifests
  - kubeadm join %s --token %s.%s --discovery-token-ca-cert-hash sha256:%s
`, k8sVersion.GitVersion, clusterEndpoint, tokenID, tokenSecret, caCertHash)

	return b64.StdEncoding.EncodeToString([]byte(bootstrapData)), nil
}

// createToken attempts to create a token with the given ID.
func createToken(ctx context.Context, c kubernetes.Interface, ttl time.Duration) (tokenID, tokenSecret string, err error) {
	token, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate bootstrap token: %w", err)
	}

	substrs := bootstraputil.BootstrapTokenRegexp.FindStringSubmatch(token)
	if len(substrs) != 3 {
		return "", "", fmt.Errorf("the bootstrap token %q was not of the form %q", token, bootstrapapi.BootstrapTokenPattern)
	}
	tokenID = substrs[1]
	tokenSecret = substrs[2]

	secretName := bootstraputil.BootstrapTokenSecretName(tokenID)
	secretToken := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: metav1.NamespaceSystem,
		},
		Type: bootstrapapi.SecretTypeBootstrapToken,
		Data: map[string][]byte{
			bootstrapapi.BootstrapTokenIDKey:               []byte(tokenID),
			bootstrapapi.BootstrapTokenSecretKey:           []byte(tokenSecret),
			bootstrapapi.BootstrapTokenExpirationKey:       []byte(time.Now().UTC().Add(ttl).Format(time.RFC3339)),
			bootstrapapi.BootstrapTokenUsageSigningKey:     []byte("true"),
			bootstrapapi.BootstrapTokenUsageAuthentication: []byte("true"),
			bootstrapapi.BootstrapTokenExtraGroupsKey:      []byte("system:bootstrappers:kubeadm:default-node-token"),
			bootstrapapi.BootstrapTokenDescriptionKey:      []byte("token generated by karpenter-provider-linode"),
		},
	}

	if _, err := c.CoreV1().Secrets(metav1.NamespaceSystem).Create(ctx, secretToken, metav1.CreateOptions{}); err != nil {
		return "", "", fmt.Errorf("failed to create bootstrap token: %w", err)
	}
	return tokenID, tokenSecret, nil
}

func (p *DefaultProvider) Get(ctx context.Context, id string, opts ...Options) (*Instance, error) {
	skipCache := option.Resolve(opts...).SkipCache
	if !skipCache {
		if i, ok := p.instanceCache.Get(id); ok {
			return i.(*Instance), nil
		}
	}

	intID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid instance id %s, %w", id, err)
	}

	linodeInstance, err := p.linodeAPI.GetInstance(ctx, intID)
	if linodego.IsNotFound(err) {
		p.instanceCache.Delete(id)
		return nil, cloudprovider.NewNodeClaimNotFoundError(err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get linode instance, %w", err)
	}

	instances, err := instancesFromLinodeInstances(ctx, []linodego.Instance{*linodeInstance})
	if err != nil {
		return nil, fmt.Errorf("getting instances from lindoe instance, %w", err)
	}

	if len(instances) != 1 {
		return nil, fmt.Errorf("expected a single instance, %w", err)
	}
	p.instanceCache.SetDefault(id, instances[0])

	return instances[0], nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*Instance, error) {
	filter := linodego.Filter{}
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("lke%d", p.clusterID))
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("%v=%v", v1alpha1.LabelLKEManaged, "true"))
	filterJSON, err := filter.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("building filter: %w", err)
	}

	linodeInstances, err := p.linodeAPI.ListInstances(ctx, linodego.NewListOptions(0, string(filterJSON)))
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	instances, err := instancesFromLinodeInstances(ctx, linodeInstances)
	for _, it := range instances {
		p.instanceCache.SetDefault(strconv.Itoa(it.ID), it)
	}

	return instances, cloudprovider.IgnoreNodeClaimNotFoundError(err)
}

func (p *DefaultProvider) Delete(ctx context.Context, id string) error {
	out, err := p.Get(ctx, id, SkipCache)
	if err != nil {
		return err
	}
	// Check if the instance is already shutting-down to reduce the number of
	// terminate-instance calls we make thereby reducing our overall QPS.
	if out.Status != linodego.InstanceDeleting {
		intID, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("invalid instance id %s, %w", id, err)
		}

		if err := p.linodeAPI.DeleteInstance(ctx, intID); err != nil {
			return err
		}
	}
	return nil
}

// NOTE: Linode's API only supports creating tags one at a time. This might be a problem if we want to add multiple tags at once.
func (p *DefaultProvider) CreateTags(ctx context.Context, id string, tags map[string]string) error {
	intId, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid instance id %s, %w", id, err)
	}
	for k, v := range tags {
		// Ensures that no more than 1 CreateTag call is made per second.
		time.Sleep(time.Second)
		if _, err := p.linodeAPI.CreateTag(ctx, linodego.TagCreateOptions{
			Linodes: []int{intId},
			Label:   fmt.Sprintf("%s=%s", k, v),
		}); err != nil {
			if linodego.IsNotFound(err) {
				return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("tagging instance, %w", err))
			}
			return fmt.Errorf("tagging instance, %w", err)
		}
	}

	return nil
}

func instancesFromLinodeInstances(ctx context.Context, instances []linodego.Instance) ([]*Instance, error) {
	if len(instances) == 0 {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance not found"))
	}
	return lo.Map(instances, func(i linodego.Instance, _ int) *Instance { inst := i; return NewInstance(ctx, &inst) }), nil
}
