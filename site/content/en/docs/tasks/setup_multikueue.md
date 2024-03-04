---
title: "Setup a MultiKueue environment"
date: 2024-02-26
weight: 9
description: >
  Additional steps needed to setup the multikueue clusters.
---

Check the concepts section for a [MultiKueue overview](/docs/concepts/multikueue/). 

## Worker Cluster

When MultiKueue dispatches a workload from the manager cluster to a worker cluster, it expects that the job's namespace and LocalQueue also exist in the worker cluster.
In other words, you should ensure that the worker cluster configuration mirrors the one of the manager cluster in terms of namespaces and LocalQueues.

### MultiKueue Specific Kubeconfig

In order to delegate the jobs in a worker cluster, the manager cluster needs to be able to create, delete, and watch workloads and their parent Jobs.

Follow these steps to create a Kubeconfig restricted to this use case.

1. Create a service account, a restricted role and role binding.

```bash
MULTIKUEUE_SA=multikueue-sa
NAMESPACE=kueue-system

echo "Creating a custom MultiKueue Role and Service Account"
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${MULTIKUEUE_SA}-role
rules:
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - batch
  resources:
  - jobs/status
  verbs:
  - get
- apiGroups:
  - jobset.x-k8s.io
  resources:
  - jobsets
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - jobset.x-k8s.io
  resources:
  - jobsets/status
  verbs:
  - get
- apiGroups:
  - kueue.x-k8s.io
  resources:
  - workloads
  verbs:
  - create
  - delete
  - get
  - list
  - watch
- apiGroups:
  - kueue.x-k8s.io
  resources:
  - workloads/status
  verbs:
  - get
  - patch
  - update
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${MULTIKUEUE_SA}-crb
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${MULTIKUEUE_SA}-role
subjects:
- kind: ServiceAccount
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
EOF

```

2. Get or create a secret associated with the service account.

```bash
SA_SECRET_NAME=$(kubectl get -n ${NAMESPACE} sa/${MULTIKUEUE_SA} -o "jsonpath={.secrets[0]..name}")
trasc marked this conversation as resolved.
if [ -z $SA_SECRET_NAME ]
then
# Create the secret and bind it to the desired SA
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
type: kubernetes.io/service-account-token
metadata:
  name: ${MULTIKUEUE_SA}
  namespace: ${NAMESPACE}
  annotations:
    kubernetes.io/service-account.name: "${MULTIKUEUE_SA}"
EOF

SA_SECRET_NAME=${MULTIKUEUE_SA}
fi
```

3. Create a Kubeconfig file

```bash
SA_TOKEN=$(kubectl get -n ${NAMESPACE} secrets/${SA_SECRET_NAME} -o "jsonpath={.data['token']}" | base64 -d)
CA_CERT=$(kubectl get -n ${NAMESPACE} secrets/${SA_SECRET_NAME} -o "jsonpath={.data['ca\.crt']}")

# Extract cluster IP from the current context
CURRENT_CONTEXT=$(kubectl config current-context)
CURRENT_CLUSTER=$(kubectl config view -o jsonpath="{.contexts[?(@.name == \"${CURRENT_CONTEXT}\"})].context.cluster}")
CURRENT_CLUSTER_ADDR=$(kubectl config view -o jsonpath="{.clusters[?(@.name == \"${CURRENT_CLUSTER}\"})].cluster.server}")

cat > worker1.kubeconfig <<EOF
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ${CA_CERT}
    server: ${CURRENT_CLUSTER_ADDR}
  name: ${CURRENT_CLUSTER}
contexts:
- context:
    cluster: ${CURRENT_CLUSTER}
    user: ${CURRENT_CLUSTER}-${MULTIKUEUE_SA}
  name: ${CURRENT_CONTEXT}
current-context: ${CURRENT_CONTEXT}
kind: Config
preferences: {}
users:
- name: ${CURRENT_CLUSTER}-${MULTIKUEUE_SA}
  user:
    token: ${SA_TOKEN}
EOF
```

## Manager Cluster

### Jobset CRD only install

As mentioned in the [MultiKueue Overview](/docs/concepts/multikueue/#jobset) section, in the manager cluster only the JobSet CRDs should be installed, you can do this by running:
```bash
kubectl apply --server-side -f  https://raw.githubusercontent.com/kubernetes-sigs/jobset/v0.4.0/config/components/crd/bases/jobset.x-k8s.io_jobsets.yaml
```

### Enable the MultiKueue feature

Enable the `MultiKueue` feature.
Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

### Create worker's Kubeconfig secret

For the next example, having the `worker1` cluster Kubeconfig stored in a file called `worker1.kubeconfig`, you can create the `worker1-secret` secret by running the following command:

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```

Check the [worker](#multikueue-specific-kubeconfig) section for details on Kubeconfig generation.

### Create a sample setup

Apply the following to create a sample setup in which the Jobs submitted in the ClusterQueue `cluster-queue` are delegated to a worker `worker1`

{{< include "/examples/multikueue/multikueue-setup.yaml" "yaml" >}}

