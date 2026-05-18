---
title: "Concurrent Admission"
linkTitle: "Concurrent Admission"
date: 2026-05-05
weight: 8
description: >
  Migrate admitted workloads to more preferred ResourceFlavors and run flavor-scoped admission checks concurrently.
---

{{< feature-state state="alpha" for_version="v0.18" >}}

Concurrent Admission lets a [Workload](/docs/concepts/workload) start on an
admitted [ResourceFlavor](/docs/concepts/resource_flavor), while Kueue keeps
admission attempts for more preferred flavors.

Concurrent Admission has two main effects:

- Kueue can migrate a running Workload to a more preferred ResourceFlavor when
  that flavor becomes available.
- Kueue can run admission checks for variants constrained to different
  ResourceFlavors concurrently, instead of waiting for one flavor's admission
  check before trying another flavor.

Use Concurrent Admission when workloads can tolerate disruption and you want to
trade extra scheduling work for faster placement, concurrent admission checks,
or migration to a preferred flavor, such as a reservation.

The intended audience for this page is [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version 0.18 or later.
- The `ConcurrentAdmission` feature gate is enabled in the Kueue controller manager.

## Enable the feature gate

`ConcurrentAdmission` is an alpha feature and is disabled by default.

Install or reconfigure Kueue with the feature gate enabled:

```diff
kind: Deployment
...
spec:
  ...
  template:
    ...
    spec:
      containers:
      - name: manager
        args:
+       - --feature-gates=ConcurrentAdmission=true
```

Follow the
[feature gates configuration instructions](/docs/installation/#change-the-feature-gates-configuration)
for details.

## Configure flavors and queues

Create a `ClusterQueue` with `.spec.concurrentAdmissionPolicy` defined multiple Resource Flavors in one ResourceGroup. The order of flavors matters - list flavors from the most preferred to the least preferred.

{{< include "examples/admin/concurrent-admission-setup.yaml" "yaml" >}}

Create the setup:

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/concurrent-admission-setup.yaml
```

With this configuration, Kueue can create one admission attempt for each flavor:

- `reservation` is the most preferred flavor.
- `on-demand` is less preferred than `reservation`.
- `spot` is the least preferred flavor.

The only supported migration mode is `TryPreferredFlavors`. In this mode, if a
Workload starts on `spot`, it means `reservation` and `on-demand` did not admit
the Workload at that time because quota was unavailable or the required
admission checks were still pending. Kueue keeps trying `reservation` and
`on-demand`. If a Variant assigned to more preferred flavor is admitted later, Kueue migrates the
Workload to that flavor.

## Limit migration to a minimum preferred flavor

If you want to limit migration, only to ResourceFlavors above a certain threshold, use `minPreferredFlavorName` API. It lets the user define what is the minimal flavor a Workload can migrate to. 

For example, using this policy a Workload can migrate from `spot` to `reservation`, and from `on-demand` to `reservation`, but not from `spot` to `on-demand`

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      minPreferredFlavorName: reservation
```

Kueue compares `minPreferredFlavorName` using the order of
`.spec.resourceGroups[*].flavors` in the `ClusterQueue`.

## Reservation with homogeneous flavors setup

Use `minPreferredFlavorName` when you have one preferred reservation flavor and
several less preferred homogeneous flavors. This lets workloads migrate to the
reservation if it becomes available, without migrating between homogeneous
fallback flavors.

For example, a Workload running on `zone-b` can migrate to `reservation`, but it
doesn't migrate to `zone-a`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  queueingStrategy: BestEffortFIFO
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
  concurrentAdmissionPolicy:
    migration:
      mode: TryPreferredFlavors
      constraints:
        minPreferredFlavorName: reservation
```

## Observe Concurrent Admission

Submit workloads to a `LocalQueue` that points to the configured `ClusterQueue`.
When Concurrent Admission is enabled for the `ClusterQueue`, Kueue marks the
original Workload as a parent and creates variant Workloads owned by that parent.
Each variant is constrained to one ResourceFlavor.

List the Workloads:

```shell
kubectl get workloads
```

Inspect a parent Workload:

```shell
kubectl describe workload WORKLOAD_NAME
```

Find the variants for a parent Workload by looking for Workloads with an owner
reference to the parent:

```shell
kubectl get workloads -o yaml
```

The parent Workload is the object that job integrations watch for admission.
Variant Workloads are internal admission attempts. Do not create or edit parent
labels or variant annotations manually.

## Constraints

Concurrent Admission currently has the following constraints:

- The feature is available on the `v1beta2` ClusterQueue API.
- The `ConcurrentAdmission` feature gate must be enabled.
- A `ClusterQueue` with `.spec.concurrentAdmissionPolicy` must use
  `BestEffortFIFO` queueing strategy. `StrictFIFO` is not supported.
- A `ClusterQueue` with `.spec.concurrentAdmissionPolicy` must have exactly one
  `resourceGroup`.
- The `resourceGroup` can contain at most 16 ResourceFlavors.
- The `concurrentAdmissionPolicy` field is immutable after the `ClusterQueue` is
  created.
- `TryPreferredFlavors` is the only supported migration mode.

## What's next?

- Learn about [ClusterQueue flavor order](/docs/concepts/cluster_queue#flavors-and-resources).
- Read the [Workload concept](/docs/concepts/workload) to understand parent and
  variant Workloads.
- Read the [API reference](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)
  for `ConcurrentAdmissionPolicy`.
