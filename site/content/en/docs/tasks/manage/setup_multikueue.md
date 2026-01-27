---
title: "Setup a MultiKueue environment"
date: 2024-02-26
weight: 9
description: >
  Additional steps needed to setup the multikueue clusters.
---

This tutorial explains how you can configure a management cluster and one worker cluster to run [JobSets](/docs/tasks/run/jobsets/#jobset-definition) and [batch/Jobs](/docs/tasks/run/jobs/#1-define-the-job) in a MultiKueue environment.

Check the concepts section for a [MultiKueue overview](/docs/concepts/multikueue/). 

Let's assume that your manager cluster is named `manager-cluster` and your worker cluster is named `worker1-cluster`.
To follow this tutorial, ensure that the credentials for all these clusters are present in the kubeconfig in your local machine.
Check the [kubectl documentation](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) to learn more about how to Configure Access to Multiple Clusters.

## In the Worker Cluster

{{% alert title="Note" color="primary" %}}
Make sure your current _kubectl_ configuration points to the worker cluster.

Run:
```bash
kubectl config use-context worker1-cluster
```
{{% /alert %}}

When MultiKueue dispatches a workload from the manager cluster to a worker cluster, it expects that the job's namespace and LocalQueue also exist in the worker cluster.
In other words, you should ensure that the worker cluster configuration mirrors the one of the manager cluster in terms of namespaces and LocalQueues.

To create the sample queue setup in the `default` namespace, you can apply the following manifest:

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

### MultiKueue Specific Kubeconfig

In order to delegate the jobs in a worker cluster, the manager cluster needs to be able to create, delete, and watch workloads and their parent Jobs.

While `kubectl` is set up to use the worker cluster, download: 
{{< include "examples/multikueue/create-multikueue-kubeconfig.sh" "bash" >}}

And run:

```bash
chmod +x create-multikueue-kubeconfig.sh
./create-multikueue-kubeconfig.sh worker1.kubeconfig
```

To create a Kubeconfig that can be used in the manager cluster to delegate Jobs in the current worker.

{{% alert title="Security Notice" color="primary" %}}

MultiKueue validates kubeconfig files to protect against known arbitrary code execution vulnerabilities.
For your security, it is strongly recommended not to use the MultiKueueAllowInsecureKubeconfigs flag.
This flag was introduced in Kueue v0.15.0 solely for backward compatibility and will be deprecated in Kueue v0.17.0.

{{% /alert %}}

### Kubeflow Installation

Install Kubeflow Trainer in the Worker cluster (see [Kubeflow Trainer Installation](https://www.kubeflow.org/docs/components/training/installation/)
for more details). Please use version v1.7.0 or a newer version for MultiKueue.

## In the Manager Cluster

{{% alert title="Note" color="primary" %}}
Make sure your current _kubectl_ configuration points to the manager cluster.

Run:
```bash
kubectl config use-context manager-cluster
```
{{% /alert %}}

### CRDs installation

For installation of CRDs compatible with MultiKueue please refer to the dedicated pages [here](/docs/tasks/run/multikueue/).

### Create worker's Kubeconfig secret

For the next example, having the `worker1` cluster Kubeconfig stored in a file called `worker1.kubeconfig`, you can create the `worker1-secret` secret by running the following command:

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```

Check the [worker](#multikueue-specific-kubeconfig) section for details on Kubeconfig generation.

### Create a sample setup

Apply the following to create a sample setup in which the Jobs submitted in the ClusterQueue `cluster-queue` are delegated to a worker `worker1`

{{< include "examples/multikueue/multikueue-setup.yaml" "yaml" >}}

Upon successful configuration the created ClusterQueue, AdmissionCheck and MultiKueueCluster will become active.

Run: 
```bash
kubectl get clusterqueues cluster-queue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}CQ - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get admissionchecks sample-multikueue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}AC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker1 -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
```

And expect an output like:
```bash
CQ - Active: True Reason: Ready Message: Can admit new workloads
AC - Active: True Reason: Active Message: The admission check is active
MC - Active: True Reason: Active Message: Connected
```

### Create a sample setup with TAS

To enable Topology-Aware Scheduling (TAS) in a MultiKueue setup, configure the worker clusters with topology levels and the manager cluster with delayed topology requests.

Worker cluster configuration:

{{< include "examples/multikueue/tas/worker-setup.yaml" "yaml" >}}

Manager cluster configuration:

{{< include "examples/multikueue/tas/manager-setup.yaml" "yaml" >}}

For a complete setup guide including local development with Kind, see the [Setup MultiKueue with Topology-Aware Scheduling](/docs/tasks/dev/setup_multikueue_tas/) guide.

## (Optional) Setup MultiKueue with Open Cluster Management

[Open Cluster Management (OCM)](https://open-cluster-management.io/) is a community-driven project focused on multicluster and multicloud scenarios for Kubernetes apps.
It provides a robust, modular, and extensible framework that helps other open source projects orchestrate, schedule, and manage workloads across multiple clusters.

The integration with OCM is an optional solution that enables Kueue users to streamline the MultiKueue setup process, automate the generation of MultiKueue specific Kubeconfig, and enhance multicluster scheduling capabilities.
For more details about this solution, please refer to this [link](https://github.com/open-cluster-management-io/ocm/tree/main/solutions/kueue-admission-check).

## Configure federated credential discovery via the ClusterProfile API

{{< feature-state state="alpha" for_version="v0.15" >}}

The [Cluster Inventory API](https://multicluster.sigs.k8s.io/) provides a standardized, vendor-neutral interface for representing a cluster's properties and status.
It defines a `ClusterProfile` resource that encapsulates the identity, metadata, and status of a specific cluster within a multi-cluster environment.

Kueue can leverage the configuration contained within a `ClusterProfile` object to obtain credentials and connect to the cluster it represents.
In practice, this means that instead of using Kubeconfig files to specify the connection to its workers, the manager cluster uses the state and
access providers contained in the existing `ClusterProfile`s. This eliminates a large portion of management required from the administrator and
allows to delegate credentialing to the entity managing the clusters (like a cloud provider).

{{% alert title="Note" color="primary" %}}

Kueue itself does not manage the `ClusterProfile` resources, which is the responsibility of the cluster manager, for example a cloud provider or a dedicated controller.

{{% /alert %}}

### Enable the `MultiKueueClusterProfile` feature gate

The `MultiKueueClusterProfile` feature gate has to be enabled for Kueue to consider `ClusterProfile` objects when connecting to clusters.
Refer to the [Installation guide](/docs/installation/#change-the-feature-gates-configuration) for instructions on configuring feature gates.

### Provision the `ClusterProfile` objects for your clusters

#### Cloud provider managed cluster inventory

When using a cloud provider that supports the automatic generation and synchronization of a cluster inventory, the `ClusterProfile` objects are
managed directly by their platform and do not require that the user sets them up manually or uses a dedicated controller.

To enable the generation of an inventory, refer to your cloud provider's documentation:
* Google Cloud Platform:
  * [Full walkthrough of MultiKueue setup with ClusterProfile](https://github.com/GoogleCloudPlatform/gke-fleet-management/blob/b9fe08386c48f84617cb8ab7b042f2790741e893/multikueue-clusterprofile/README.md).
  * [Inventory generation documentation](https://docs.cloud.google.com/kubernetes-engine/fleet-management/docs/generate-inventory-for-integrations).

When using cloud provider managed cluster inventory, you should expect a `ClusterProfile` object to be created in your MultiKueue manager cluster,
one per cluster (including the manager itself).

For example:
```yaml
apiVersion: multicluster.x-k8s.io/v1alpha1
kind: ClusterProfile
metadata:
  labels:
    x-k8s.io/cluster-manager: <cloud-provider-service-name>
  name: multikueue-worker-1
  namespace: kueue-system
spec:
  clusterManager:
    name: <cloud-provider-service-name>
  displayName: multikueue-worker-1
status:
  accessProviders:
  - cluster:
      server: <cluster-url>
    name: <access-provider-name>
```

Where:
* `cloud-provider-service-name` is the name of the service within the cloud provider that manages the cluster. For example `gke-fleet`.
* `cluster-url` is the URL of the cluster's control plane.
* `access-provider-name` is the name of the provider of the cluster's credentials. For example `google`.

{{% alert title="Note" color="primary" %}}

The `ClusterProfile`s have to be provisioned within the Kueue system namespace (`kueue-system` by default).

{{% /alert %}}

#### Manually created cluster inventory

In principle, the `ClusterProfile` objects can be provisioned without the use of a cloud provider.

Firstly, the `ClusterProfile` CRDs have to be manually installed within the cluster:
```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/cluster-inventory-api/refs/heads/main/config/crd/bases/multicluster.x-k8s.io_clusterprofiles.yaml
```

Next, the `ClusterProfile` objects can be created:
```yaml
apiVersion: multicluster.x-k8s.io/v1alpha1
kind: ClusterProfile
metadata:
  name: worker1-cluster
  namespace: kueue-system
spec:
  ...
status:
  accessProviders:
  - name: <access-provider-name>
    cluster:
      ...
```

{{% alert title="Note" color="primary" %}}

The `ClusterProfile` access providers are within the object's `status` subresource and, in principle, should be managed by the cluster manager
(i.e. the entity provisioning the clusters, like a cloud provider) and not created manually.

They also have to be provisioned within the Kueue system namespace (`kueue-system` by default).

{{% /alert %}}

### Install the access provider plugin

The access provider defined in `ClusterProfile` depends upon an executable plugin being available to the
Kueue controller manager pods running within the MultiKueue manager cluster.
It's the responsibility of the Kueue administrator to make sure that the required command is available.

An example plugin can be found [here](https://github.com/kubernetes-sigs/cluster-inventory-api/tree/445319b6307a88778b930e154ed3e2f38d85a689/cmd/secretreader-plugin) (secret reader) or [here](https://cloud.google.com/blog/products/containers-kubernetes/kubectl-auth-changes-in-gke) (Google Cloud Platform).

#### Mount the executable with `initContainers`

The [`initContainers`](https://kubernetes.io/docs/concepts/workloads/pods/init-containers/) field within the pods specification
can be used to populate a volume with the required plugin. The image used by the init container has to be built beforehand.

The `kueue-controller-manager` container within the Kueue manager deployment can then mount this volume in its filesystem to access the executable.
For example:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kueue-controller-manager
  namespace: kueue-system
spec:
  template:
    spec:
      initContainers:
      - name: add-auth-plugin
        image: <plugin-image>
        command: ["cp", "<plugin-command>", "/plugins/<plugin-command>"]
        volumeMounts:
        - name: clusterprofile-plugins
          mountPath: "/plugins"
      containers:
      - name: manager
        volumeMounts:
        - name: clusterprofile-plugins
          mountPath: "/plugins"
      volumes:
      - name: clusterprofile-plugins
        emptyDir: {}
```

Where:
* `plugin-image` is the image reference of an image that contains the plugin executable.
* `plugin-command` is the name of the executable. For example `gcp-auth-plugin`.

#### (Kubernetes 1.35+) Mount the executable using an image volume

The ability to [mount content from OCI registries into containers](https://kubernetes.io/docs/tasks/configure-pod-container/image-volumes/) reached beta in Kubernetes 1.35. This feature streamlines some aspects of the `initContainers` approach.

For example:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kueue-controller-manager
  namespace: kueue-system
spec:
  template:
    spec:
      containers:
      - name: manager
        volumeMounts:
        - name: clusterprofile-plugins
          mountPath: "/plugins"
      volumes:
      - name: clusterprofile-plugins
        image:
          reference: <plugin-image>
          pullPolicy: IfNotPresent
```

Where:
* `plugin-image` is the image reference of an image that contains the plugin executable.

The files inside the `plugin-image` will be accessible under the `mountPath` (in this case `/plugins`)
and can be called analogously to the `initContainers` approach (`/plugins/<plugin-command>`).

#### Build a custom Kueue manager image

Alternatively, a custom Kueue manager image can be built to include the plugin executable.
The image reference in the Kueue manager deployment should then point to the user-managed custom image.

### Define the credentials provider in the Kueue manager config

To connect the access providers specified in the `ClusterProfile`s with the mounted plugin,
an entry within the Kueue configuration has to be created:

```yaml
apiVersion: v1
data:
  controller_manager_config.yaml: |
    ...
    multiKueue:
      clusterProfile:
        credentialsProviders:
        - name: <access-provider-name>
          execConfig:
            apiVersion: client.authentication.k8s.io/v1beta1
            command: /plugins/<plugin-command>
            interactiveMode: Never
kind: ConfigMap
metadata:
  labels:
    app.kubernetes.io/name: kueue
    control-plane: controller-manager
  name: kueue-manager-config
  namespace: kueue-system
```

Where:
* `access-provider-name` is the name of the provider of the cluster's credentials.
It has to match the `accessProviders` name in the relevant `ClusterProfile`s.
* `plugin-command` is the name of the executable within the manager container.

This definition will configure the `ClusterProfile`s using the `access-provider-name` to retrieve
cluster credentials via the `plugin-command` executable.

### Link `MultiKueueCluster` objects to their corresponding `ClusterProfile`

When using the `ClusterProfile` API for authentication, the `MultiKueueCluster` objects have to reference a `ClusterProfile` via the `clusterProfileRef` field,
instead of providing `kubeconfig` directly.

Here's an example `MultiKueueCluster` object using a `clusterProfileRef`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: MultiKueueCluster
metadata:
  name: worker1-cluster
spec:
  clusterSource:
    clusterProfileRef:
      name: worker1-cluster
```
