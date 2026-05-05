---
title: "Set Up Concurrent Admission"
linkTitle: "Concurrent Admission"
date: 2026-05-05
weight: 8
description: >
  Configure Kueue to admit workloads by trying multiple resource flavors concurrently.
---

{{< feature-state state="alpha" for_version="v0.18" >}}

Concurrent Admission lets Kueue try multiple [ResourceFlavors](/docs/concepts/resource_flavor)
for the same [Workload](/docs/concepts/workload) at the same time. A Workload can
start on the first flavor that leads to admission, while Kueue can continue
trying more preferred flavors and migrate the Workload if a better flavor later
becomes available.

Use Concurrent Admission when workloads can tolerate disruption and you want to
trade extra scheduling work for faster placement or for migration to a preferred
flavor, such as a reservation.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version 0.18 or later.
- The `ConcurrentAdmission` feature gate is enabled in the Kueue controller manager.

## Enable the feature gate

`ConcurrentAdmission` is an alpha feature and is disabled by default.

Install or reconfigure Kueue with the feature gate enabled:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  ConcurrentAdmission: true
```

Follow the
[custom configuration installation instructions](/docs/installation#install-a-custom-configured-released-version)
for details on changing the Kueue configuration.

## Configure flavors and queues

Create a `ClusterQueue` with multiple flavors in one `resourceGroup` and enable
`.spec.concurrentAdmissionPolicy`. The flavor order matters: list flavors from
the most preferred to the least preferred.

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
Workload starts on `spot`, Kueue keeps trying `reservation` and `on-demand`. If a
more preferred flavor is admitted later, Kueue migrates the Workload to that
flavor.

## Limit migration to a minimum preferred flavor

Use `minPreferredFlavorName` when you want migration only to a specific flavor or
to a flavor that is more preferred than it.

For example, this policy allows migration to `reservation`, but not from `spot`
to `on-demand`:

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      minPreferredFlavorName: reservation
```

Kueue compares `minPreferredFlavorName` using the order of
`.spec.resourceGroups[*].flavors` in the `ClusterQueue`.

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
