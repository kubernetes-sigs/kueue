---
title: "Run A Wrapped Custom Job"
linkTitle: "Custom Job"
date: 2025-01-14
weight: 7
description: >
  Use an AppWrapper to Run a Custom Job on Kueue.
---

This page shows how to use [AppWrappers](https://project-codeflare.github.io/appwrapper/) to make
Kueue's scheduling and resource management capabilities available to Job types that do not have a dedicated
Kueue integration.  For Jobs that use `PodSpecTemplates` in their definition, this can provide
a significantly easier approach than [building a custom integration](/docs/tasks/dev/integrate_a_custom_job)
to enable the use of Kueue with a custom Job type.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.11.0 version or newer and AppWrapper v1.0.0 or newer.

2. Follow the steps in [Run AppWrappers](/docs/tasks/run/appwrappers/#before-you-begin)
to learn how to enable and configure the `codeflare.dev/appwrapper` integration.

## Example using LeaderWorkerSets as the Custom Job

[LeaderWorkerSets](https://github.com/kubernetes-sigs/lws) are an exanple of a type that
uses PodSpecTemplates but currently does not have a Kueue integration.

1. Follow the [install](https://github.com/kubernetes-sigs/lws/blob/main/docs/setup/install.md)
instructions for LeaderWorkerSets.

2. Edit the `appwrapper-manager-role` `ClusterRole` to add the stanza below to allow
the appwrapper controller to manipulate LeaderWorketSets.
```yaml
- apiGroups:
  - leaderworkerset.x-k8s.io
  resources:
  - leaderworkersets
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
```

3. The AppWrapper containing the LeaderWorkerSet is shown below.
In particular, notice how the `replicas` and `path` of each element of the `podSets` array
corresponds to a `PodSpecTemplate` and replica count within `template`.
This gives the AppWrapper controller enough information to enable
it to "understand" the wrapped resource and provide Kueue the information it needs to
manage it.

{{< include "examples/jobs/appwrapper-leaderworkerset-sample.yaml" "yaml" >}}

