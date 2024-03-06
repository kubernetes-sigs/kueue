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

To create the sample queue setup in the `default` namespace apply:

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

{{< include "examples/multikueue/multikueue-setup.yaml" "yaml" >}}

