---
title: "Set Up Concurrent Admission"
linkTitle: "Concurrent Admission"
date: 2026-05-05
weight: 8
description: >
  Configure Kueue to migrate admitted workloads to more preferred ResourceFlavors and run flavor-scoped admission checks concurrently.
---

{{< feature-state state="alpha" for_version="v0.18" >}}

This page shows how to set up [Concurrent Admission](/v0.19/docs/concepts/concurrent_admission)
for a ClusterQueue.

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/v0.19/docs/installation) in version 0.18 or later.

## Enable the feature gate

`ConcurrentAdmission` is an alpha feature and is disabled by default.

Follow the
[feature gates configuration instructions](/v0.19/docs/installation/#change-the-feature-gates-configuration)
to enable the `ConcurrentAdmission` feature gate in the Kueue controller manager.

## Configure flavors and queues

Create a `ClusterQueue` with `.spec.concurrentAdmissionPolicy` defined and
multiple ResourceFlavors in one ResourceGroup. The order of flavors matters:
list ResourceFlavors from the most preferred to the least preferred.

{{< include "v0.19/examples/admin/concurrent-admission-setup.yaml" "yaml" >}}

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
`on-demand`. If a Variant assigned to a more preferred flavor is admitted later,
Kueue migrates the Workload to that flavor.

## Limit migration to a last acceptable flavor

If you want to limit migration to ResourceFlavors above a certain threshold, use
the `lastAcceptableFlavorName` API. It lets you define the last acceptable flavor
a Workload can migrate to.

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

## Reservation with homogeneous flavors setup

Use `lastAcceptableFlavorName` when you have one preferred reservation flavor and
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

## Observe Concurrent Admission

Submit workloads to a `LocalQueue` that points to the configured `ClusterQueue`.
When Concurrent Admission is enabled for the `ClusterQueue`, Kueue marks the
original Workload as a Parent and creates Variant Workloads owned by that Parent.
Each Variant is constrained to one ResourceFlavor.

List the Workloads:

```shell
kubectl get workloads
```

Inspect a Parent Workload:

```shell
kubectl describe workload WORKLOAD_NAME
```

Find Parent Workloads by looking for Workloads with the Parent label
`kueue.x-k8s.io/concurrent-admission-parent`:

```shell
kubectl get workloads -l kueue.x-k8s.io/concurrent-admission-parent=true
```

A Parent Workload can have metadata like this:

```yaml
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/concurrent-admission-parent: "true"
```

Inspect the Variant metadata and check that it references the Parent Workload:

```shell
kubectl get workload VARIANT_WORKLOAD_NAME -o yaml
```

A Variant Workload can have metadata like this:

```yaml
metadata:
  name: sample-job-variant-spot-a2342
  ownerReferences:
  - apiVersion: kueue.x-k8s.io/v1beta2
    kind: Workload
    name: sample-job
    uid: 7a9a0d5e-2c9c-4b3a-9c62-2b64a72f6a3f
    controller: true
    blockOwnerDeletion: true
```

The Parent Workload is the object that job integrations watch for admission.
Variant Workloads are internal admission attempts. Do not create or edit Parent
labels or Variant annotations manually.

## What's next?

- Read the [Concurrent Admission concept](/v0.19/docs/concepts/concurrent_admission).
- Read the [API reference](/v0.19/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)
  for `ConcurrentAdmissionPolicy`.
