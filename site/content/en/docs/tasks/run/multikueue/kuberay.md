---
title: "Run KubeRay Jobs in Multi-Cluster"
linkTitle: "KubeRay"
weight: 4
date: 2025-03-19
description: >
  Run a MultiKueue scheduled KubeRay Jobs.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.11.0 and for KubeRay Operator at least v1.3.1.

See [KubeRay Operator Installation](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) for installation and configuration details of KubeRay Operator.

{{% alert title="Note" color="primary" %}}
Before the [ManagedBy feature](https://github.com/ray-project/kuberay/issues/2544) was supported in Kueue (below v0.11.0), the installation of KubeRay Operator in the <b>Manager Cluster</b> must be limited to CRDs only.

To install the CRDs run:
```bash
kubectl create -k "github.com/ray-project/kuberay/ray-operator/config/crd?ref=v1.3.0"
```
{{% /alert %}}

## MultiKueue integration

Once the setup is complete you can test it by running a RayJob [`ray-job-sample.yaml`](/docs/tasks/run/rayjobs/#example-rayjob).

{{% alert title="Note" color="primary" %}}
Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for KubeRay Jobs (RayJob, RayCluster, RayService). 

This allows the KubeRay Operator to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip Pod creation. 

The pods are created and the actual computation will happen on the mirror copy of the Job on the selected worker cluster. 
The mirror copy of the Job does not have the field set.
{{% /alert %}}

## RayService autoscaling and in-place updates

For an [elastic RayService](/docs/tasks/run/rayservices/#autoscaling-aka-intreeautoscaling),
enable `MultiKueueRayInTreeAutoscaling` on the management cluster in addition to
`ElasticJobsViaWorkloadSlices` on the management and worker clusters. Set
`spec.upgradeStrategy.type: None` to keep a single active RayCluster.

With `MultiKueueRemoteSpecSync` enabled on the management cluster (the default),
changes to `spec.serveConfigV2` are forwarded to the existing worker RayService.
KubeRay applies the Serve configuration to the active RayCluster in place.
Any resulting worker autoscaling changes are reflected back to the management
cluster and accounted for through workload slices. Scale-up still requires
quota admission for the additional worker Pods.

This does not forward changes to `spec.rayClusterConfig` or enable upgrades that
create a second RayCluster. Zero-downtime upgrades with active and pending
RayClusters remain unsupported by this integration.

{{% alert title="Ray History Server" color="primary" %}}
Using [Ray History Server](https://docs.ray.io/en/latest/cluster/kubernetes/user-guides/kuberay-history-server.html)
with MultiKueue requires KubeRay v1.7.0 or later and Ray v2.55 or later.
Configure the collector at the path for the workload kind:

- RayCluster: `spec.historyServerOptions`
- RayJob: `spec.rayClusterSpec.historyServerOptions`
- RayService: `spec.rayClusterConfig.historyServerOptions`

Install the KubeRay v1.7.0 or later CRDs on the management and worker clusters,
and enable the KubeRay `RayClusterHistoryServer` feature gate on every worker
operator. Kueue propagates the options to the worker cluster and accounts for
the collector sidecar resources in every Ray head and worker Pod.

These additional requirements apply only to History Server. Other KubeRay
workloads retain the minimum versions listed above. Kueue doesn't enforce the
worker operator version or its feature gates; older CRDs can't persist
`historyServerOptions`, so no collector is configured.
{{% /alert %}}
