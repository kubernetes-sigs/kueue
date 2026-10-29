---
title: "Satellite Projects"
linkTitle: "Satellite Projects"
weight: 85
description: >
  Community-driven extensions and experimental controllers built on top of Kueue.
---

Satellite projects complement Kueue's core functionality with specialized controllers and integrations maintained outside the main binary.

## Experimental

The following experimental controllers are developed alongside Kueue but are not part of the main release. They demonstrate patterns for extending Kueue and provide opt-in capabilities for advanced use cases.

### Kueue Populator

[Kueue Populator](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/kueue-populator) automatically creates `LocalQueue` resources in namespaces that match a `ClusterQueue`'s `namespaceSelector`. This simplifies provisioning for multi-tenant clusters where `LocalQueue`s should follow namespace lifecycle.

**Status:** Experimental  
**Build & Deploy:** Independent binary and Helm chart

### Kueue Priority Booster

[Kueue Priority Booster](https://github.com/kubernetes-sigs/kueue/tree/main/cmd/experimental/kueue-priority-booster) implements time-sharing fairness by gradually reducing the effective priority of long-running workloads via the `kueue.x-k8s.io/priority-boost` annotation. This enables same-priority workloads to preempt each other after a configurable window under the `LowerPriority` preemption policy.

**Status:** Experimental  
**Build & Deploy:** Independent binary and Helm chart
