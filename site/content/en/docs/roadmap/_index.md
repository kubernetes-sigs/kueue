---
title: "Roadmap"
linkTitle: "Roadmap"
weight: 8
date: 2022-02-17
description: >
    This page describes a high-level overview of the main priorities for the project.
---

### Roadmap for 2023

These features are in expected order of release:

- Job preemption to reclaim borrowed quota and to accommodate high priority jobs [#83](https://github.com/kubernetes-sigs/kueue/issues/83), this is planned for v0.3
- Cooperative preemption support for workloads that implement checkpointing [#477](https://github.com/kubernetes-sigs/kueue/issues/477)
- Flavor assignment strategies, e.g. _minimizing cost_ vs _minimizing borrowing_ [#312](https://github.com/kubernetes-sigs/kueue/issues/312)
- Integration with cluster-autoscaler for guaranteed resource provisioning
- Integration with common custom workloads [#74](https://github.com/kubernetes-sigs/kueue/issues/74):
  - Kubeflow (TFJob, MPIJob, etc.)
  - Spark
  - Ray
  - Workflows (Tekton, Argo, etc.)

### Roadmap for long-term

These features are in no particular order:

- Budget support [#28](https://github.com/kubernetes-sigs/kueue/issues/28)
- Dashboard for management and monitoring for administrators
- Multi-cluster support
