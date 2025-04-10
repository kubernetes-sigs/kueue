---
title: "Run An AppWrapper"
linkTitle: "AppWrappers"
date: 2025-01-08
weight: 6
description: >
  Run an AppWrapper on Kueue.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [AppWrappers](https://project-codeflare.github.io/appwrapper/).

AppWrappers provide a flexible and workload-agnostic mechanism for enabling Kueue to manage a group of Kubernetes resources as a single
logical unit without requiring any Kueue-specific support by the controllers of those resources.

AppWrappers are designed to harden workloads by providing an additional level of automatic fault detection and recovery. The
AppWrapper controller monitors the health of the workload and if corrective actions are not taken by the primary resource controllers
within specified deadlines, the AppWrapper controller will orchestrate workload-level retries and resource deletion to ensure that either the
workload returns to a healthy state or is cleanly removed from the cluster and its quota freed for use by other workloads.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Make sure you are using Kueue v0.11.0 version or newer and AppWrapper v1.0.2 or newer.

2. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

3. Because AppWrappers were initially designed as an external framework for Kueue, you need to install the
Standalone configuration of the AppWrapper controller. This disables the AppWrapper controller's instance of Kueue's
GenericJob Reconciller. One way to do this is by doing
```
kustomize build "https://github.com/project-codeflare/appwrapper/config/standalone?ref=v1.0.2"
```
A future release of AppWrapper will change its default configuration to disable its copy of the GenericJob Reconciller.

## AppWrapper definition

When running AppWrappers on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the AppWrapper.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload are computed by combining the resource needs of each wrapper component.

## Example AppWrapper containing a PyTorchJob

The AppWrapper looks like the following:

{{< include "examples/appwrapper/pytorch-sample.yaml" "yaml" >}}

{{% alert title="Note" color="primary" %}}
The example above comes from [here](https://raw.githubusercontent.com/project-codeflare/appwrapper/refs/heads/main/samples/wrapped-pytorch-job.yaml)
and only has the `queue-name` label changed.
{{% /alert %}}

## Example AppWrapper containing a Deployment

The AppWrapper looks like the following:

{{< include "examples/appwrapper/deployment-sample.yaml" "yaml" >}}
