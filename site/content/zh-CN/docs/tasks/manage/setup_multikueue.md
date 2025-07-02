---
title: "Setup a MultiKueue environment"
date: 2024-02-26
weight: 9
description: >
  Additional steps needed to setup the multikueue clusters.
---

This tutorial explains how you can configure a management cluster and one worker cluster to run [JobSets](/docs/tasks/run_jobsets/#jobset-definition) and [batch/Jobs](/docs/tasks/run_jobs/#1-define-the-job) in a MultiKueue environment.

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

## (Optional) Setup MultiKueue with Open Cluster Management

[Open Cluster Management (OCM)](https://open-cluster-management.io/) is a community-driven project focused on multicluster and multicloud scenarios for Kubernetes apps.
It provides a robust, modular, and extensible framework that helps other open source projects orchestrate, schedule, and manage workloads across multiple clusters.

The integration with OCM is an optional solution that enables Kueue users to streamline the MultiKueue setup process, automate the generation of MultiKueue specific Kubeconfig, and enhance multicluster scheduling capabilities.
For more details about this solution, please refer to this [link](https://github.com/open-cluster-management-io/ocm/tree/main/solutions/kueue-admission-check).
