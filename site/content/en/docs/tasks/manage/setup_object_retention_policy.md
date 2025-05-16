---
title: "Setup garbage-collection of workload"
date: 2025-05-16
weight: 11
description: >
  Configure automatic garbage-collection of finished and deactivated Workloads
  by defining retention policies in your KueueConfiguration.
---

This guide shows you how to enable and configure optional object retention
policies in Kueue to automatically delete finished or deactivated Workloads
after a specified time. By default, Kueue leaves all Workload objects in cluster
indefinitely; with object retention you can free up etcd storage and reduce
Kueue’s memory footprint.

## Prerequisites

- A running Kueue installation at **v0.12** or newer.
- The `ObjectRetentionPolicies` feature-gate enabled in the Kueue controller manager. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

## Set up a retention policy

Follow the instructions described
[here](/docs/installation#install-a-custom-configured-released-version) to
install a release version by extending the configuration with the following
fields:

```yaml
      objectRetentionPolicies:
        workloads:
          afterFinished: "1m"
          afterDeactivatedByKueue: "1m"
```

### Workload Retention Policy

The retention policy for Workloads is defined in the
`objectRetentionPolicies.workloads` field.
It contains the following optional fields:
- `afterFinished`: Duration after which finished Workloads are deleted.
- `afterDeactivatedByKueue`: Duration after which any Kueue-deactivated Workloads (such as a Job, JobSet, or other custom workload types) are deleted.


## Example

---

### Scenario A: Successfully finished Workload

1. **Configure** Kueue with a 1m retention policy:

```yaml
  objectRetentionPolicies:
    workloads:
      afterFinished: "1m"
```

2. **Submit** a simple Workload that should finish normally:

```bash
   kubectl apply -f successful-workload.yaml
   kubectl get workloads --all-namespaces
```

3. Watch the status transition to `Finished`. After ~1 minute, Kueue will automatically delete it:

```bash
   # ~1m after Finished
   kubectl get workloads --all-namespaces
   # <successful-workload not found>
```

---

### Scenario B: Evicted Workload via `waitForPodsReady`

1. **Configure** Kueue [deployment](/docs/installation#install-a-custom-configured-released-version) to have more resources available than the node can provide:

```yaml
        resources:
          limits:
            cpu: "100" # or any value greater than the node's capacity
```

2. **Configure** Kueue with a 1m retention policy and enable [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready.md):

```yaml
  objectRetentionPolicies:
    workloads:
      afterDeactivatedByKueue: "1m"
  waitForPodsReady:
    enable: true
    timeout: 2m
    recoveryTimeout: 1m
    blockAdmission: true
    requeuingStrategy:
      backoffLimitCount: 0
```

3. **Submit** a Workload that requests more than is available on the node:

```bash
   kubectl apply -f limited-workload.yaml
   kubectl get workloads --all-namespaces
```

4. Without all Pods ready, Kueue evicts and deactivates the Workload:

```bash
   kubectl get workloads --all-namespaces
   # NAME                    QUEUE    RESERVED IN   ADMITTED   FINISHED   AGE
   # limited-workload                               False                 2m
```

5. ~1 minute after eviction, the deactivated Workload is garbage-collected:

```bash
   # ~1m after eviction
   kubectl get workloads --all-namespaces
   # <limited-workload not found>
```

## Notes

- `afterDeactivatedByKueue` is the duration to wait after *any* Kueue-managed Workload
  (such as a Job, JobSet, or other custom workload types)
  has been marked as deactivated by Kueue before automatically deleting it.
  Deletion of deactivated workloads may cascade to objects not created by Kueue,
  since deleting the parent Workload owner (e.g. JobSet) can trigger garbage-collection of dependent resources.
- If a retention duration is mis-configured (invalid duration),
  the controller will fail to start.
- Deletion is handled synchronously in the reconciliation loop; in
  clusters with thousands of expired Workloads it may take time to
  remove them all on first startup.
