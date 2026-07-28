---
title: "Setup default WorkloadPriorityClass"
date: 2026-04-27
weight: 11
description: >
  Setup a default WorkloadPriorityClass to assign a queueing priority to jobs that don't specify one.
---

{{< feature-state state="alpha" for_version="v0.18" >}}

This page describes how to setup a default WorkloadPriorityClass to ensure that all Kueue-managed workloads receive a consistent queueing priority,
even if the `kueue.x-k8s.io/priority-class` label is not specified explicitly.

## Setup default WorkloadPriorityClass

WorkloadPriorityClassDefaulting is a feature that allows the use of a WorkloadPriorityClass with name `default` as the default WorkloadPriorityClass
for workloads that do not have the `kueue.x-k8s.io/priority-class` label.

To use this feature:

- enable the `WorkloadPriorityClassDefaulting` feature gate in the Kueue configuration.
  Check the [Installation](/v0.19/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

- create a WorkloadPriorityClass with the name `default`:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: WorkloadPriorityClass
metadata:
  name: default
value: 1000
description: "Default workload priority"
```

That's all! Now, to test the feature, create a Job without the `kueue.x-k8s.io/priority-class` label. Observe that the Job is updated with the `kueue.x-k8s.io/priority-class: default` label.

Note that jobs that already have the `kueue.x-k8s.io/priority-class` label won't be modified.
