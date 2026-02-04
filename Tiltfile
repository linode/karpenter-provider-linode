# Load some extensions.
load("ext://k8s_attach", "k8s_attach")
load("ext://helm_resource", "helm_resource")

# Set a longer upsert timeout to avoid apply timeouts during heavy operations.
# Set a longer upsert/apply timeout to avoid "apply command timed out" errors.
update_settings(k8s_upsert_timeout_secs=600)

# Allow any context that starts with "lke*".
if k8s_context().startswith("lke"):
    allow_k8s_contexts(k8s_context())

# Install CRDs for Karpenter to work
helm_resource(
    "karpenter-crd",
    "./charts/karpenter-crd",
    namespace="kube-system",
    flags=[
        "--create-namespace",
        "--wait",
    ],
    labels=["karpenter-crd"],
)
k8s_resource('karpenter-crd', pod_readiness='ignore')

# Install Karpenter itself once the CRDs are present for the controller manager to monitor
local_resource(
    'karpenter',
    cmd="helm upgrade --install --namespace kube-system --create-namespace karpenter charts/karpenter --set settings.clusterName=${CLUSTER_NAME} --set apiToken=${LINODE_TOKEN} --set controller.image.repository=${KO_DOCKER_REPO} --set controller.image.tag=$(git rev-parse --abbrev-ref HEAD) --wait",
    env={'LINODE_TOKEN': os.getenv("LINODE_TOKEN"), 'CLUSTER_NAME': os.getenv("CLUSTER_NAME"), 'KO_DOCKER_REPO': os.getenv("KO_DOCKER_REPO")},
    labels=["karpenter"],
    resource_deps=["karpenter-crd"],
)

k8s_yaml("./examples/v1/simple.yaml")
k8s_resource(
    new_name="karpl-nodepool-nodeclass",
    objects=["default:linodenodeclass", "default:nodepool"],
    labels=["karpenter"],
    resource_deps=["karpenter-crd"],
)
