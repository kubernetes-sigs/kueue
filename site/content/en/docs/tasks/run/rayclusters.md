---
title: "Run A RayCluster"
linkTitle: "RayClusters"
date: 2024-08-07
weight: 10
description: >
  Run a RayCluster with Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [RayCluster](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Choose a KubeRay version compatible with your Kueue release. See
   [KubeRay compatibility](#kuberay-compatibility) for the tested version and
   additional requirements for individual features.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) for installation and configuration details of KubeRay.

{{% alert title="Note" color="primary" %}}
In order to use RayCluster, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -l control-plane=controller-manager -n kueue-system`.
{{% /alert %}}

## KubeRay compatibility

On Kueue's `main` branch, the default end-to-end test configuration uses
**KubeRay v1.7.0**, the
`github.com/ray-project/kuberay/ray-operator` version pinned in
[`go.mod`](https://github.com/kubernetes-sigs/kueue/blob/main/go.mod).
The test targets derive `KUBERAY_VERSION` from this dependency and use it to
install the operator and its CRDs. For an older Kueue release, check the
dependency and documentation at that release's Git tag rather than relying on
the version documented for `main`.

The tested version is not a guarantee that every older or newer KubeRay version
is compatible. Kueue does not currently define an N-3 KubeRay compatibility
policy. In particular, the previously documented KubeRay v1.1.0 minimum describes
when basic RayJob and RayCluster integration became available, not the versions
tested with current Kueue releases.

Some features require newer KubeRay APIs in addition to the applicable Kueue
feature gates and configuration:

| Feature | KubeRay requirement | Details |
| --- | --- | --- |
| Basic RayService integration using nested suspend | v1.3.0 or newer | Historical API requirement for Kueue v0.17.0 and newer; see [RayService suspend control](/docs/tasks/run/rayservices/#c-suspend-control). |
| RayService top-level `spec.suspend` | v1.7.0 or newer | `KubeRayServiceUsingTopLevelSuspend` is enabled by default starting in Kueue v0.21.0. Disable it when using older KubeRay versions. |
| MultiKueue with RayJob or RayCluster | v1.3.1 or newer is recommended for the `spec.managedBy` workflow | See [MultiKueue setup](/docs/tasks/run/multikueue/kuberay/). |
| MultiKueue with RayService | v1.6.0 or newer for `spec.managedBy` | The field was introduced in [KubeRay v1.6.0](https://github.com/ray-project/kuberay/releases/tag/v1.6.0). The top-level suspend requirement also applies when that Kueue feature gate is enabled. |
| Ray History Server with MultiKueue | v1.7.0 or newer, and Ray v2.55 or newer | Install matching CRDs on the management and worker clusters and enable `RayClusterHistoryServer` on worker operators; see [History Server requirements](/docs/tasks/run/multikueue/kuberay/#multikueue-integration). |

These feature requirements are not a tested compatibility range for all Kueue
and KubeRay releases. With MultiKueue, check both the CRDs on the management and
worker clusters and the operators on the worker clusters: an older CRD can drop
fields that a newer feature needs.

## RayCluster definition

When running [RayClusters](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayCluster configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec`.

```yaml
spec:
  headGroupSpec:
    template:
      spec:
        containers:
          - resources:
              requests:
                cpu: "1"
  workerGroupSpecs:
    - template:
        spec:
          containers:
            - resources:
                requests:
                  cpu: "1"
```

Note that a RayCluster will hold resource quotas while it exists. For optimal resource management, you should delete a RayCluster that is no longer in use.

### c. Suspend control

Kueue controls the `spec.suspend` field of the RayCluster. When a RayCluster is admitted by Kueue, Kueue will unsuspend it by setting `spec.suspend` to `false`, regardless of its previous value.

### d. Limitations
- Limited Worker Groups: Because a Kueue workload can have a maximum of 18 PodSets, the maximum number of `spec.workerGroupSpecs` is 17
- In-Tree Autoscaling Constraints: Autoscaling is only supported for [elastic](/docs/concepts/elastic_workload) RayCluster objects. To enable in-tree autoscaling:

  1. Activate the `ElasticJobsViaWorkloadSlices` feature gate.
  2. Annotate the RayCluster object with:

     ```yaml
     metadata:
       annotations:
         kueue.x-k8s.io/elastic-job: "true"
     ```
  3. Enable the Ray autoscaler of your RayCluster object by setting:

     ```yaml
     spec:
       enableInTreeAutoscaling: true
     ```

## Example RayCluster

The RayCluster looks like the following:

{{< include "examples/jobs/ray-cluster-sample.yaml" "yaml" >}}

You can submit a Ray Job using the [CLI](https://docs.ray.io/en/latest/cluster/running-applications/job-submission/quickstart.html) or log into the Ray Head and execute a job following this [example](https://ray-project.github.io/kuberay/deploy/helm-cluster/#end-to-end-example) with kind cluster.

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/ray-project/kuberay/v1.4.2/ray-operator/config/samples/ray-cluster.complete.yaml)
and only has the `queue-name` label added and requests updated.
{{% /alert %}}
