---
title: "Opt-in Namespace Management"
date: 2025-11-25
weight: 15
description: >
  Configure Kueue to only manage workloads in explicitly labeled namespaces using the ManagedJobsNamespaceSelectorAlwaysRespected feature gate.
---

This page describes how to configure Kueue to enforce opt-in namespace management, ensuring that only workloads in explicitly labeled namespaces are reconciled by Kueue.

## Overview

The `ManagedJobsNamespaceSelectorAlwaysRespected` feature gate controls whether the `managedJobsNamespaceSelector` restricts the reconciliation of all workloads, regardless of whether they have a `kueue.x-k8s.io/queue-name` label.

When this feature gate is **disabled** (default behavior prior to v0.13):
- If `manageJobsWithoutQueueName` is false, `managedJobsNamespaceSelector` has no effect: Kueue will manage exactly those instances of supported Kinds that have a `queue-name` label.
- If `manageJobsWithoutQueueName` is true, then Kueue will (a) manage all instances of supported Kinds that have a `queue-name` label and (b) will manage all instances of supported Kinds that do not have a `queue-name` label if they are in namespaces that match `managedJobsNamespaceSelector`.

When this feature gate is **enabled**:
- The namespace selector check happens **first**, before any other reconciliation logic.
- If a workload's namespace does not match the `managedJobsNamespaceSelector` (and the selector is not nil), the workload will not be reconciled by Kueue — regardless of whether it has a `queue-name` label or the value of `manageJobsWithoutQueueName`.
- If a workload's namespace matches the selector (or if `managedJobsNamespaceSelector` is nil), normal reconciliation logic applies:
  - If `manageJobsWithoutQueueName=false`: Kueue will manage exactly those instances of supported Kinds that have a `queue-name` label.
  - If `manageJobsWithoutQueueName=true`: Kueue will manage all instances of supported Kinds with or without `queue-name` label.

This change brings the `batch/v1/Job` integration into consistent alignment with `Pod`, `Deployment`, and `StatefulSet` integrations, which already do not act on resources in non-opted-in namespaces.

## Feature Gate Status

- **Alpha**: v0.13
- **Beta**: v0.15

## Before you begin

- Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).
- Understand how to configure [`manageJobsWithoutQueueName`](/docs/tasks/manage/enforce_job_management/manage_jobs_without_queue_name/).

## Enabling the Feature Gate

To enable this feature gate, you need to modify the Kueue manager configuration to include the feature gate flag:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta1
    kind: ControllerManagerConfig
    featureGates:
      ManagedJobsNamespaceSelectorAlwaysRespected: true
```

Or if using command-line flags:

```yaml
args:
  - "--feature-gates=ManagedJobsNamespaceSelectorAlwaysRespected=true"
```

## Configuration

Once the feature gate is enabled, you need to configure the `managedJobsNamespaceSelector` to specify which namespaces should be managed by Kueue.

### Labeling Namespaces

Namespaces that should be managed by Kueue must be labeled appropriately. For example, you can use the `managed-by-kueue` label:

```bash
kubectl label namespace my-namespace managed-by-kueue=true
```

### Configuring the Selector

Configure the `managedJobsNamespaceSelector` in your Kueue Configuration to match the labeled namespaces. Since this feature gate enforces an opt-in model, you should use `matchLabels` to select namespaces that you have explicitly labeled:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
metadata:
  name: config
  namespace: kueue-system
manageJobsWithoutQueueName: true
managedJobsNamespaceSelector:
  matchLabels:
    managed-by-kueue: "true"
```

**Note**: When `ManagedJobsNamespaceSelectorAlwaysRespected` is enabled, only namespaces that match the selector will be reconciled. Namespaces without the required labels will not be managed by Kueue, regardless of whether workloads in those namespaces have a `queue-name` label.

## Expected Behavior

When the feature gate is enabled:

1. **Namespace selector check happens first**: Before checking for `queue-name` labels or evaluating `manageJobsWithoutQueueName`, Kueue first checks if the workload's namespace matches the `managedJobsNamespaceSelector`.

2. **Workloads in non-managed namespaces**: Any workload in a namespace that does not match the `managedJobsNamespaceSelector` (and the selector is not nil) will not be reconciled by Kueue and will be ignored — regardless of whether it has a `queue-name` label or the value of `manageJobsWithoutQueueName`.

3. **Workloads in managed namespaces** (or when selector is nil): After passing the namespace selector check, normal reconciliation logic applies:
   - If `manageJobsWithoutQueueName=false`: Only workloads with a `queue-name` label will be managed.
   - If `manageJobsWithoutQueueName=true`: All workloads (with or without `queue-name` label) will be managed.

## Use Cases

### Opt-in Namespace Management

Cluster administrators can use this feature to enforce an opt-in model where only explicitly labeled namespaces are managed by Kueue. This prevents users from bypassing quota enforcement by manually adding the `queue-name` label to workloads in unmanaged namespaces.

Example scenario:
- Cluster admin labels specific namespaces with `managed-by-kueue: true`
- Users cannot bypass Kueue management by adding `queue-name` labels to workloads in unlabeled namespaces
- Only workloads in opted-in namespaces are reconciled by Kueue

## Related Documentation

- [KEP-3589: Uniformly filter manageJobsWithoutQueueNames by namespace](https://github.com/kubernetes-sigs/kueue/tree/main/keps/3589-manage-jobs-selectively)
- [Setup manageJobsWithoutQueueName](/docs/tasks/manage/enforce_job_management/manage_jobs_without_queue_name/)
- [Setup Job Admission Policy](/docs/tasks/manage/enforce_job_management/setup_job_admission_policy/)
