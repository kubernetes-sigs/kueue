---
title: "Opt-in Namespace Management"
date: 2025-11-25
weight: 15
description: >
  Configure Kueue to only manage workloads in explicitly labeled namespaces.
---

This page describes how to configure Kueue to enforce opt-in namespace management, ensuring that only workloads in explicitly labeled namespaces are reconciled by Kueue.

## Before you begin

- Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).
- Understand how to configure [`manageJobsWithoutQueueName`](/docs/tasks/manage/enforce_job_management/manage_jobs_without_queue_name/).
- Learn how to [change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration).

## Why Use Opt-in Namespace Management?

Opt-in namespace management allows cluster administrators to enforce strict quota controls by ensuring that Kueue only manages workloads in explicitly designated namespaces. This provides several key benefits:

- **Prevents quota bypass**: Users cannot bypass Kueue's quota system by manually adding `queue-name` labels to workloads in unmanaged namespaces. Only workloads in opted-in namespaces are ever reconciled by Kueue, regardless of how they are labeled.
- **Explicit control**: Cluster administrators have full control over which namespaces are subject to Kueue management by simply labeling namespaces.
- **Consistent behavior**: This brings `batch/v1.Job` integration into alignment with `Pod`, `Deployment`, and `StatefulSet` integrations, which already enforce namespace-based filtering.

## Configuration

### Step 1: Label Namespaces

Label the namespaces that should be managed by Kueue. For example, use the `managed-by-kueue` label:

```bash
kubectl label namespace my-namespace managed-by-kueue=true
```

### Step 2: Configure the Selector

Configure the `managedJobsNamespaceSelector` in your Kueue Configuration to match the labeled namespaces. Use `matchLabels` to select namespaces that you have explicitly labeled:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
metadata:
  name: config
  namespace: kueue-system
manageJobsWithoutQueueName: true
managedJobsNamespaceSelector:
  matchLabels:
    managed-by-kueue: "true"
```

### Step 3: Enable the Feature Gate

This step is optional if you're using Kueue v0.15 or later, as the `ManagedJobsNamespaceSelectorAlwaysRespected` feature gate is enabled by default. If you're using an older version of Kueue, you need to explicitly enable this feature gate. See the [feature gates for alpha and beta features](/docs/installation/#feature-gates-for-alpha-and-beta-features) section for the feature gate status.

## Feature Gate Details

The `ManagedJobsNamespaceSelectorAlwaysRespected` feature gate controls whether the `managedJobsNamespaceSelector` restricts the reconciliation of all workloads, regardless of whether they have a `kueue.x-k8s.io/queue-name` label.

### How It Works

When this feature gate is **enabled**:
- The namespace selector check happens **first**, before any other reconciliation logic.
- If a workload's namespace does not match the `managedJobsNamespaceSelector` (and the selector is not nil), the workload will not be reconciled by Kueue — regardless of whether it has a `queue-name` label or the value of `manageJobsWithoutQueueName`.
- If a workload's namespace matches the selector (or if `managedJobsNamespaceSelector` is nil), normal reconciliation logic applies:
  - If `manageJobsWithoutQueueName=false`: Kueue will manage exactly those instances of supported Kinds that have a `queue-name` label.
  - If `manageJobsWithoutQueueName=true`: Kueue will manage all instances of supported Kinds with or without `queue-name` label.

When this feature gate is **disabled** (default behavior prior to v0.13):
- If `manageJobsWithoutQueueName` is false, `managedJobsNamespaceSelector` has no effect: Kueue will manage exactly those instances of supported Kinds that have a `queue-name` label.
- If `manageJobsWithoutQueueName` is true, then Kueue will (a) manage all instances of supported Kinds that have a `queue-name` label and (b) will manage all instances of supported Kinds that do not have a `queue-name` label if they are in namespaces that match `managedJobsNamespaceSelector`.

## Related Documentation

- [KEP-3589: Uniformly filter manageJobsWithoutQueueNames by namespace](https://github.com/kubernetes-sigs/kueue/tree/main/keps/3589-manage-jobs-selectively)
- [Setup manageJobsWithoutQueueName](/docs/tasks/manage/enforce_job_management/manage_jobs_without_queue_name/)
- [Setup Job Admission Policy](/docs/tasks/manage/enforce_job_management/setup_job_admission_policy/)
