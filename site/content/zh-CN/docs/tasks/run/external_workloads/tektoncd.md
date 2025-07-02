---
title: "Run A Tekton Pipeline"
linkTitle: "Tekton Pipeline"
date: 2025-02-01
weight: 7
description: >
  Integrate Kueue with Tekton Pipelines.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Tekton pipelines](https://tekton.dev/docs/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

We demonstrate how to support scheduling Tekton Pipelines Tasks in Kueue based on the [Plain Pod](/docs/tasks/run_plain_pods) integration, where every Pod from a Pipeline is represented as a single independent Plain Pod.

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

1. Follow the steps in [Run Plain Pods](docs/tasks/run/plain_pods/#before-you-begin) to learn how to enable and configure the `pod` integration.

1. Check [Administrator cluster quotas](/docs/tasks/manage/administer_cluster_quotas/) for details on the initial Kueue step.

1. Your cluster has tekton pipelines [installed](https://tekton.dev/docs/installation/pipelines/).


## Tekton Background

Tekton has the concept of [Pipelines](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelines/), [Tasks](https://tekton.dev/vault/pipelines-v0.59.x-lts/tasks/) and [PipelineRun](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelineruns/).

A pipeline consists of tasks. Tasks and pipelines must be created before running a pipeline.

A PipelineRun runs the pipeline.

A TaskRun runs a single task. PipelineRuns will reuse TaskRuns to run each task in a pipeline.

### Tekton Defintions

As a simple example, we will define two tasks named sleep and hello:

{{< include "examples/pod-based-workloads/tekton-sleep-task.yaml" "yaml" >}}

{{< include "examples/pod-based-workloads/tekton-hello-task.yaml" "yaml" >}}

A pipeline composes these tasks.

{{< include "examples/pod-based-workloads/tekton-pipeline.yaml" "yaml" >}}

## a. Targeting a single LocalQueue

If you want every task to target a single [local queue](/docs/concepts/local_queue),
it should be specified in the `metadata.label` section of the PipelineRun configuration.

{{< include "examples/pod-based-workloads/tekton-pipeline-run.yaml" "yaml" >}}

This will inject the kueue label on every pod of the pipeline. Kueue will gate the pods once you are over the quota limits.

## Limitations 

- Kueue will only manage pods created by Tekton.
- Each pod in a Workflow will create a new Workload resource and must wait for admission by Kueue.
- There is no way to ensure that a Workflow will complete before it is started. If one step of a multi-step Workflow does not have
available quota, Tekton pipelines will run all previous steps and then wait for quota to become available.
