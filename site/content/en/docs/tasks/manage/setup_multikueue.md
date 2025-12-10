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

## Setup MultiKueue with ClusterProfile API

{{< feature-state state="alpha" for_version="v0.15" >}}

The [ClusterProfile API](https://multicluster.sigs.k8s.io/concepts/cluster-profile-api/) provides a standardized, vendor-neutral interface for presenting cluster information. It allows defining cluster access information in a standardized `ClusterProfile` object and using credential plugins for authentication.

### Enable MultiKueueClusterProfile feature gate
Enable the `MultiKueueClusterProfile` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.

### Create ClusterProfile objects

If you are using a cloud provider, refer to the documentation on how to generate ClusterProfile objects (e.g. [GKE](https://docs.cloud.google.com/kubernetes-engine/fleet-management/docs/generate-inventory-for-integrations)). Alternatively, you can manually install the `ClusterProfile` CRD and objects for your clusters.

To install the `ClusterProfile` CRD, run:
```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/cluster-inventory-api/refs/heads/main/config/crd/bases/multicluster.x-k8s.io_clusterprofiles.yaml
```

To create a `ClusterProfile` object for `worker1-cluster`, run:
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
  - name: ${PROVIDER_NAME}
    cluster:
      server: https://${SERVER_ENDPOINT}
      certificate-authority-data: ${CERTIFICATE_AUTHORITY_DATA}
```

### Configure Kueue Manager

Next, configure the controller manager config map with the credentials providers.

```yaml
apiVersion: v1
data:
  controller_manager_config.yaml: |
    ...
    multiKueue:
      clusterProfile:
        credentialsProviders:
        - name: ${PROVIDER_NAME}
          execConfig:
            apiVersion: client.authentication.k8s.io/v1beta1
            command: /plugins/${PLUGIN_COMMAND}
kind: ConfigMap
metadata:
  labels:
    app.kubernetes.io/name: kueue
    control-plane: controller-manager
  name: kueue-manager-config
  namespace: kueue-system
```

### Install Required Plugins

If your credentials provider requires an executable plugin, you must make it available to the Kueue manager.

#### Add plugins via volume mounts

You can use an `initContainer` to add the plugin to a shared `emptyDir` volume before the Kueue manager starts. The `kueue-controller-manager` container can then mount this volume to access the plugin.

Here is an example patch for the `kueue-controller-manager` deployment that adds a custom authentication plugin:

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
        image: ${PLUGIN_IMAGE}
        command: ["cp", "${PLUGIN_COMMAND}", "/plugins/${PLUGIN_COMMAND}"]
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

This patch does the following:
1.  Adds an `initContainer` that copies the `${PLUGIN_COMMAND}` from its container image to the `/plugins` directory in the shared volume.
2.  Adds an `emptyDir` volume named `clusterprofile-plugins` to the pod.
3.  Mounts the `clusterprofile-plugins` volume to the `manager` container, making the plugin available at `/plugins/${PLUGIN_COMMAND}`.

#### Build a custom image

Alternatively, you can build a custom Kueue manager image that includes your plugin. You would then update your Kueue deployment to use this new image.

### Configure MultiKueueCluster objects

When using the `ClusterProfile` API for authentication, configure your `MultiKueueCluster` objects to reference a `ClusterProfile` via the `clusterProfileRef` field, instead of providing `kubeconfig` directly.

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
