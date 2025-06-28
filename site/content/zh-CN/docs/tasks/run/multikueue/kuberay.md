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
Note: Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for KubeRay Jobs (RayJob, RayCluster, RayService). 

This allows the KubeRay Operator to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip Pod creation. 

The pods are created and the actual computation will happen on the mirror copy of the Job on the selected worker cluster. 
The mirror copy of the Job does not have the field set.
{{% /alert %}}
