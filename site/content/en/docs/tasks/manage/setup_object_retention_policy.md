---
title: "Setup object retention"
date: 2025-05-16
weight: 11
description: >
  Configure automatic garbage-collection of finished and deactivated Workloads
  by defining retention policies in your KueueConfiguration.
---

This guide shows you how to enable and configure optional object retention
policies in Kueue to automatically delete finished or deactivated Workloads
after a specified time. By default, Kueue leaves all Workload objects in etcd
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
          afterFinished: "1h"
          afterDeactivatedByKueue: "1h"
```

{{% alert title="Note" color="primary" %}}
If you update an existing Kueue installation you may need to restart the
`kueue-controller-manager` pod in order for Kueue to pick up the updated
configuration. In that case run:

```shell
kubectl delete pods --all -n kueue-system
```
{{% /alert %}}

### Workload Retention Policy

The retention policy for Workloads is defined in the
`objectRetentionPolicies.workloads` field.
It contains the following optional fields:
- `afterFinished`: Duration after which finished Workloads are deleted.
- `afterDeactivatedByKueue`: Duration after which any Kueue-deactivated Workloads (such as a Job, JobSet, or other custom workload types) are deleted.


## Example

1. **Submit** a sample Workload (or JobSet) through Kueue.
2. **Watch** the Workload reach a terminal state:

```bash
kubectl get workloads --all-namespaces
```

3. Once you see the Workload status become `Finished` (or `Deactivated`),
   note the timestamp. After the configured duration (e.g. 1 hour), the Workload object will disappear:

```bash
# after the retention period
kubectl get workloads --all-namespaces
# <no matching items>
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
