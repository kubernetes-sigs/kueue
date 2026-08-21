---
title: "Setup Quota Release Strategy"
date: 2026-08-15
weight: 6
description: >
  Configure when Kueue releases quota for terminating workloads.
---

When Kueue manages workloads, it acquires quota from ClusterQueues upon workload admission. When workloads are preempted or deleted, Kueue releases this quota so other workloads can be admitted.

Kueue provides a global `quotaReleaseStrategy` configuration in the `kueue-configuration` ConfigMap to control the timing of quota release during workload termination.

The intended audience for this page is [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The `kubectl` command-line tool has communication with your cluster.
- Kueue is installed in version 0.10.0 or later.

## Quota Release Strategies

Kueue supports two quota release strategies:

1. **`OnTerminating`** (Default):
   Quota is released as soon as a workload is marked terminating (for example, when all underlying pods receive a `deletionTimestamp`). This preserves fast readmission for batch jobs and preempted workloads.

2. **`OnTerminal`**:
   Quota is held until all underlying pods reach a terminal phase (`Succeeded` or `Failed`) and release hardware resources. This strategy is critical for Topology-Aware Scheduling (TAS) and hardware-constrained workloads (such as GPUs or specialized accelerators) where physical node capacity must be fully freed before new workloads can be scheduled on the same hardware.

## Configuring quotaReleaseStrategy

You can configure `quotaReleaseStrategy` at the top level of your Kueue `Configuration`:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
quotaReleaseStrategy: OnTerminal
```

{{% alert title="Note" color="primary" %}}
If you update an existing Kueue installation, restart the `kueue-controller-manager` pod to pick up the updated configuration:

```shell
kubectl delete pods --all -n kueue-system
```
{{% /alert %}}
