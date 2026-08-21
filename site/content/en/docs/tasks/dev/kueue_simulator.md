---
title: "Kueue simulator (design)"
linkTitle: "Kueue simulator"
date: 2026-03-31
weight: 8
description: >
  Design direction and interim options for simulating Kueue without a full cluster.
---

This page tracks work for a **Kueue simulator**: running queueing, admission,
preemption, and fair-sharing scenarios **without** the cost of a full Kubernetes
cluster and real Pods. The feature is tracked in GitHub issue
[#10168](https://github.com/kubernetes-sigs/kueue/issues/10168).

## Design

The Kubernetes Enhancement Proposal (KEP) for the simulator lives in the
repository:

- [KEP-10168: Kueue Simulator](https://github.com/kubernetes-sigs/kueue/tree/main/keps/10168-kueue-simulator)

The KEP describes phased delivery, the scenario/configuration surface, and how
the implementation should relate to existing performance tooling.

## What exists today

The closest supported workflow today is the **scalability / performance scheduler**
stack under [`test/performance/scheduler`](https://github.com/kubernetes-sigs/kueue/tree/main/test/performance/scheduler):

- **Runner** — generates Kueue objects from YAML, mimics workload execution, and
  records statistics.
- **MinimalKueue** — core Kueue controllers and scheduler against a small API
  footprint (often via [envtest](https://book.kubebuilder.io/reference/envtest.html)).

See the [Scalability test README](https://github.com/kubernetes-sigs/kueue/blob/main/test/performance/scheduler/README.md)
for commands such as `make run-performance-scheduler` and
`make test-performance-scheduler`.

That stack is aimed at **maintainers and regression testing**; the simulator KEP
describes evolving this into a **documented, user-facing** capability with a
stable scenario format.

## Relationship to KWOK

[KWOK](https://kwok.sigs.k8s.io/) builds large **fake** Kubernetes clusters.
The Kueue simulator focuses first on **Kueue decision accuracy**, not full
cluster emulation; KWOK remains a complementary option when you need many Nodes
and Pods at the Kubernetes API level.
