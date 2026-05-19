---
title: "Concurrent Admission"
linkTitle: "Concurrent Admission"
date: 2026-05-05
weight: 8
description: >
  Migrate admitted workloads to more preferred ResourceFlavors and run flavor-scoped admission checks concurrently.
---

{{< feature-state state="alpha" for_version="v0.18" >}}

Concurrent Admission lets Kueue keep several admission attempts for the same
[Workload](/docs/concepts/workload), each constrained to a different
[ResourceFlavor](/docs/concepts/resource_flavor). This lets a Workload start on
an admitted flavor while Kueue continues pursuing more preferred flavors.

Concurrent Admission consists of two main components:

- **Event-driven migration:** Kueue can move a running Workload to a more
  preferred ResourceFlavor as soon as that flavor becomes available.
- **Concurrent multi-flavor pursuit:** A Workload can independently pursue
  multiple ResourceFlavors at the same time.

Kueue implements this by creating variants of the original Workload, which is
called the parent Workload. Each variant is a copy of the parent Workload
assigned to a specific ResourceFlavor, allowing variants to try scheduling
concurrently and independently on their respective flavors. The order of flavors
within a ClusterQueue's resource groups dictates their preference, with the first
flavor being the most preferred.

Use Concurrent Admission when workloads can tolerate disruption and you want to
trade extra scheduling work for faster placement, concurrent admission checks,
or migration to a preferred flavor, such as a reservation.

## Flavor preference and migration

The only supported migration mode is `TryPreferredFlavors`. In this mode, if a
Workload starts on a less preferred flavor, Kueue keeps pursuing variants for
more preferred flavors. If a variant assigned to a more preferred flavor is
admitted later, Kueue migrates the Workload to that flavor.

If you want to limit migration to flavors above a certain preference threshold,
use the `lastAcceptableFlavorName` API. It defines the last acceptable flavor a
Workload can migrate to.

For example, using this policy a Workload can migrate from `spot` to
`reservation`, and from `on-demand` to `reservation`, but not from `spot` to
`on-demand`:

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      lastAcceptableFlavorName: reservation
```

In a reservation with homogeneous fallback flavors setup,
`lastAcceptableFlavorName` lets workloads migrate to the reservation if it
becomes available, without migrating between homogeneous fallback flavors.

For example, a Workload running on `zone-b` can migrate to `reservation`, but it
doesn't migrate to `zone-a`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  concurrentAdmissionPolicy:
    migration:
      mode: TryPreferredFlavors
      constraints:
        lastAcceptableFlavorName: reservation
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: reservation
      resources:
      - name: cpu
        nominalQuota: 4
      - name: memory
        nominalQuota: 16Gi
    - name: zone-a
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
    - name: zone-b
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
    - name: zone-c
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
  admissionChecksStrategy:
    admissionChecks:
    - name: capacity-check
      onFlavors: [zone-a, zone-b, zone-c]
```

## Parent and variant Workloads

When Concurrent Admission is enabled for the `ClusterQueue`, Kueue marks the
original Workload as a parent and creates variant Workloads owned by that parent.
Each variant is constrained to one ResourceFlavor.

Variant Workloads are labeled with the parent label
`kueue.x-k8s.io/concurrent-admission-parent`, which stores the parent Workload
name. A variant Workload can have metadata like this:

```yaml
metadata:
  name: sample-job-variant-spot-a2342
  labels:
    kueue.x-k8s.io/concurrent-admission-parent: sample-job
  ownerReferences:
  - apiVersion: kueue.x-k8s.io/v1beta2
    kind: Workload
    name: sample-job
    uid: 7a9a0d5e-2c9c-4b3a-9c62-2b64a72f6a3f
    controller: true
    blockOwnerDeletion: true
```

The parent Workload is the object that job integrations watch for admission.
Variant Workloads are internal admission attempts. Do not create or edit parent
labels or variant annotations manually.

## Constraints

Concurrent Admission currently has the following constraints:

- The feature is available on the `v1beta2` ClusterQueue API.
- The `ConcurrentAdmission` feature gate must be enabled.
- A `ClusterQueue` with `.spec.concurrentAdmissionPolicy` must use the
  `BestEffortFIFO` queueing strategy. `StrictFIFO` is not supported.
- A `ClusterQueue` with `.spec.concurrentAdmissionPolicy` must have exactly one
  `resourceGroup`.
- The `resourceGroup` can contain at most 16 ResourceFlavors.
- The `concurrentAdmissionPolicy` field is immutable after the `ClusterQueue` is
  created.
- `TryPreferredFlavors` is the only supported migration mode.

## What's next?

- [Set up Concurrent Admission](/docs/tasks/manage/setup_concurrent_admission).
- Learn about [ClusterQueue flavor order](/docs/concepts/cluster_queue#flavors-and-resources).
- Read the [Workload concept](/docs/concepts/workload) to understand parent and
  variant Workloads.
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)
  for `ConcurrentAdmissionPolicy`.
