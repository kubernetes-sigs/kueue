---
title: "Run Plain Pod in Multi-Cluster"
linkTitle: "Plain Pod"
weight: 2
date: 2025-02-17
description: >
  Run a MultiKueue scheduled Plain Pod.
---

## Before you begin

1. Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

2. Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
to learn how to enable and configure the `pod` integration.

Pods created on the manager cluster are automatically gated and receive live status updates from their remote counterparts

{{< feature-state state="beta" for_version="v0.11.0" >}}

## Example

Once the setup is complete you can test it by running the examples below:

1. Single plain pod
{{< include "examples/pods-kueue/kueue-pod.yaml" "yaml" >}}

2. Group of pods
{{< include "examples/pods-kueue/kueue-pod-group.yaml" "yaml" >}}