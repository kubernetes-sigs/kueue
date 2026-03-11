---
title: "Setup MultiKueue Development Environment"
date: 2026-01-13
weight: 5
description: >
  Configure MultiKueue for local development and testing.
---

This tutorial explains how you can configure a manager cluster and worker clusters to run jobs with [Topology-Aware Scheduling (TAS)](/docs/tasks/run/leaderworkerset/#configure-topology-aware-scheduling) in a MultiKueue environment. We also outline the automated steps using Kind for local testing.

Check the concepts section for a [MultiKueue overview](/docs/concepts/multikueue/) and [Topology-Aware Scheduling overview](/docs/concepts/topology_aware_scheduling/).

## Setup MultiKueue with E2E Test Cluster

The [e2e test development mode](/docs/contribution_guidelines/testing/#dev-mode-recommended) can be used to maintain a MultiKueue cluster setup and run end-to-end tests
against it without recreating and tearing it down each time.

For example:
```sh
E2E_MODE=dev make kind-image-build test-multikueue-e2e
```

For more information about the DEV mode, refer to the testing documentation.

## Setup MultiKueue with TAS

{{% alert title="Note" color="primary" %}}
MultiKueue with Topology-Aware Scheduling is available since Kueue v0.15.0.
{{% /alert %}}

### Automated Setup

For a quick setup, download and run the setup script:

```bash
git clone https://github.com/kubernetes-sigs/kueue.git
cd kueue/examples/multikueue/dev
./setup-kind-multikueue-tas.sh
```

The script will set up a complete MultiKueue environment that is additionally configured with TAS.

Alternatively, follow the manual steps below.

### Step 1: Create Kind Clusters

Create three Kind clusters (manager and two workers):

```bash
# Manager cluster
cat <<EOF | kind create cluster --name manager --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
  labels:
    instance-type: on-demand
    cloud.provider.com/node-group: tas-node
EOF

# Worker 1 cluster
cat <<EOF | kind create cluster --name worker1 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
  labels:
    instance-type: on-demand
    cloud.provider.com/node-group: tas-node
EOF

# Worker 2 cluster
cat <<EOF | kind create cluster --name worker2 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
  labels:
    instance-type: on-demand
    cloud.provider.com/node-group: tas-node
EOF
```

{{% alert title="Note" color="primary" %}}
This setup uses hostname-level topology (`kubernetes.io/hostname`). The `cloud.provider.com/node-group` label is used by the ResourceFlavor for node selection.
{{% /alert %}}

Verify all clusters are created:

```bash
kind get clusters
# Expected output:
# manager
# worker1
# worker2
```

### Step 2: Install Kueue on All Clusters

```bash
KUEUE_VERSION=v0.15.0
kubectl --context kind-manager apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml
kubectl --context kind-worker1 apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml
kubectl --context kind-worker2 apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${KUEUE_VERSION}/manifests.yaml

for ctx in kind-manager kind-worker1 kind-worker2; do
  kubectl --context $ctx wait --for=condition=available --timeout=300s deployment/kueue-controller-manager -n kueue-system
done
```

### Step 3: Configure Manager for Kind

Enable the feature gate for insecure kubeconfigs and configure integrations:

```bash
# Enable feature gate (required for Kind clusters with insecure-skip-tls-verify)
kubectl --context kind-manager patch deployment kueue-controller-manager -n kueue-system --type='json' \
  -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--feature-gates=MultiKueueAllowInsecureKubeconfigs=true"}]'

# Configure to use only batch/job integration
cat > /tmp/kueue-integrations-patch.yaml <<'EOF'
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    health:
      healthProbeBindAddress: :8081
    metrics:
      bindAddress: :8443
    webhook:
      port: 9443
    leaderElection:
      leaderElect: true
      resourceName: c1f6bfd2.kueue.x-k8s.io
    controller:
      groupKindConcurrency:
        Job.batch: 5
        Pod: 5
        Workload.kueue.x-k8s.io: 5
        LocalQueue.kueue.x-k8s.io: 1
        Cohort.kueue.x-k8s.io: 1
        ClusterQueue.kueue.x-k8s.io: 1
        ResourceFlavor.kueue.x-k8s.io: 1
    clientConnection:
      qps: 50
      burst: 100
    integrations:
      frameworks:
      - "batch/job"
EOF
kubectl --context kind-manager patch configmap kueue-manager-config -n kueue-system --patch-file=/tmp/kueue-integrations-patch.yaml

# Restart controller to apply changes
kubectl --context kind-manager rollout restart deployment/kueue-controller-manager -n kueue-system
kubectl --context kind-manager rollout status deployment/kueue-controller-manager -n kueue-system --timeout=180s
```

### Step 4: Configure Worker Clusters

Apply the worker configuration with TAS:

{{< include "examples/multikueue/tas/worker-setup.yaml" "yaml" >}}

```bash
kubectl --context kind-worker1 apply -f examples/multikueue/tas/worker-setup.yaml
kubectl --context kind-worker2 apply -f examples/multikueue/tas/worker-setup.yaml
```

Create ServiceAccounts and kubeconfigs for MultiKueue on both workers:

```bash
SERVICE_ACCOUNT="multikueue-sa"
for cluster in worker1 worker2; do
  # Create ServiceAccount
  kubectl --context kind-${cluster} create sa ${SERVICE_ACCOUNT} -n kueue-system

  # Create ClusterRoles
  kubectl --context kind-${cluster} create clusterrole multikueue-role \
    --verb=create,delete,get,list,watch \
    --resource=jobs.batch,workloads.kueue.x-k8s.io,pods

  kubectl --context kind-${cluster} create clusterrole multikueue-role-status \
    --verb=get,patch,update \
    --resource=jobs.batch/status,workloads.kueue.x-k8s.io/status,pods/status

  # Create ClusterRoleBindings
  kubectl --context kind-${cluster} create clusterrolebinding multikueue-crb \
    --clusterrole=multikueue-role \
    --serviceaccount=kueue-system:${SERVICE_ACCOUNT}

  kubectl --context kind-${cluster} create clusterrolebinding multikueue-crb-status \
    --clusterrole=multikueue-role-status \
    --serviceaccount=kueue-system:${SERVICE_ACCOUNT}

  # Create a secret bound to the new service account
  kubectl --context "kind-${cluster}" apply -f - <<EOF 
apiVersion: v1
kind: Secret
metadata:
  name: multikueue-sa-token
  namespace: kueue-system
  annotations:
    kubernetes.io/service-account.name: "${SERVICE_ACCOUNT}"
type: kubernetes.io/service-account-token
EOF

  # Note: service account token is stored base64-encoded in the secret but must
  # be plaintext in kubeconfig
  TOKEN=$(kubectl --context "kind-${cluster}" get secret multikueue-sa-token -n kueue-system -o jsonpath='{.data.token}' | base64 --decode)

  # Create kubeconfig
  cat > ${cluster}.kubeconfig <<EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: https://${cluster}-control-plane:6443
  name: ${cluster}
contexts:
- context:
    cluster: ${cluster}
    user: ${SERVICE_ACCOUNT}
  name: ${cluster}
current-context: ${cluster}
users:
- name: ${SERVICE_ACCOUNT}
  user:
    token: ${TOKEN}
EOF
done
```

### Step 5: Configure Manager Cluster

Create secrets and apply configuration:

```bash
kubectl --context kind-manager create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
kubectl --context kind-manager create secret generic worker2-secret -n kueue-system --from-file=kubeconfig=worker2.kubeconfig
```

{{< include "examples/multikueue/tas/manager-setup.yaml" "yaml" >}}

```bash
kubectl --context kind-manager apply -f examples/multikueue/tas/manager-setup.yaml
```

### Step 6: Validate the Setup

Verify that all components are active:

```bash
kubectl --context kind-manager get clusterqueue,admissioncheck,multikueuecluster
```

Expected output:

```
NAME                                        COHORT   PENDING WORKLOADS
clusterqueue.kueue.x-k8s.io/cluster-queue            0

NAME                                          AGE
admissioncheck.kueue.x-k8s.io/multikueue-ac   5m

NAME                                       CONNECTED   AGE
multikueuecluster.kueue.x-k8s.io/worker1   True        5m
multikueuecluster.kueue.x-k8s.io/worker2   True        5m
```

### Step 7: Submit a TAS Job

Submit a sample job that requires topology-aware scheduling:

{{< include "examples/multikueue/dev/sample-tas-job.yaml" "yaml" >}}

```bash
kubectl --context kind-manager apply -f examples/multikueue/dev/sample-tas-job.yaml
kubectl --context kind-manager get workloads.kueue.x-k8s.io -n default
```

Expected output showing the workload was admitted and delegated to a worker cluster:

```
NAME               QUEUE        RESERVED IN   ADMITTED   AGE
tas-sample-job     user-queue   worker1       True       5s
implicit-tas-job   user-queue   worker2       True       5s
```

## Cleanup

Delete the Kind clusters:

```bash
kind delete clusters manager worker1 worker2
```

## Next Steps

- Learn more about [Topology-Aware Scheduling](/docs/concepts/topology_aware_scheduling/)
- Explore [MultiKueue concepts](/docs/concepts/multikueue/)
