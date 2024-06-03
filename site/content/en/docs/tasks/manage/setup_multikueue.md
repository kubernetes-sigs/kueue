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

{{% alert title="Note" color="note" %}}
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

## In the Manager Cluster

{{% alert title="Note" color="note" %}}
Make sure your current _kubectl_ configuration points to the manager cluster.

Run:
```bash
kubectl config use-context manager-cluster
```
{{% /alert %}}

### JobSet installation

If you are using Kueue in version 0.7.0 or newer install the JobSet on the
management cluster (see [JobSet Installation](https://jobset.sigs.k8s.io/docs/installation/)
for more details). We recommend using JobSet 0.5.1+ for MultiKueue.

{{% alert title="Warning" color="warning" %}}
If you are using an older version of Kueue than 0.7.0, only install the JobSet
CRD in the management cluster. You can do this by running:
```bash
kubectl apply --server-side -f https://raw.githubusercontent.com/kubernetes-sigs/jobset/v0.5.1/config/components/crd/bases/jobset.x-k8s.io_jobsets.yaml
```
{{% /alert %}}

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
