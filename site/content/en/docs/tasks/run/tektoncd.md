---
title: "Run A Tekton Pipeline"
date: 2024-01-03
weight: 7
description: >
  Integrate Kueue with TektonCD Pipelines.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [TektonCD pipelines](https://tekton.dev/docs/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

As of the writing of this doc, Kueue doesn't support TektonCD Pipelines [Pipelines](https://github.com/tektoncd/pipeline/blob/main/docs/README.md) resources directly,
but you can take advantage of the ability for Kueue to [manage plain pods](/docs/tasks/run_plain_pods) to integrate them.

## Before you begin

1. By default, the integration for `v1/pod` is not enabled.
   Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
   and enable the `pod` integration.

   The pod integration for TektonCD Pipelines could look like:

   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
     podOptions:
       # You can change namespaceSelector to define in which
       # namespaces kueue will manage the tektoncd pods.
       namespaceSelector:
         matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: NotIn
           values: [ kube-system, kueue-system ]
       # Tekton pipelines uses the app.kubernetes.io/managed-by label to
       # keep track of pods it manages. We will use that as a hint for Kueue
       # to find Tekton pods.
       podSelector:
         matchExpressions:
         - key: app.kubernetes.io/managed-by
           operator: In
           values: [ "tekton-pipelines" ]
   ```

2. Pods that belong to other API resources managed by Kueue are excluded from being queued by `pod` integration.
   For example, pods managed by `batch/v1.Job` won't be managed by `pod` integration.

3. Check [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial Kueue setup.

4. Your cluster has tekton pipelines [installed](https://tekton.dev/docs/installation/pipelines/).

## Motivation

Let's say you are providing a build platform for a software delivery service.
During a busy time of a year, you have many engineers creating build pipelines and your cluster is at capacity due to the number of builds created.
One can use Kueue to limit the number of pods that are created by using the pod integration.
Kueue provides quota limits on core resources so one could create a clusterqueue that limits the number of pods.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["pods"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "pods"
        nominalQuota: 2
```

## Tekton Background

Tekton has the concept of [Pipelines](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelines/), [Tasks](https://tekton.dev/vault/pipelines-v0.59.x-lts/tasks/) and [PipelineRun](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelineruns/).

A pipeline consists of tasks. Tasks and pipelines must be created before running a pipeline.

A PipelineRun runs the pipeline.

A TaskRun runs a single task. PipelineRuns will reuse TaskRuns to run each task in a pipeline.

### Tekton Defintions

As a simple example, we will define two tasks named sleep and hello:

Tasks:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: sleep
spec:
  steps:
    - name: echo
      image: alpine
      script: |
        #!/bin/sh
        sleep 100
```

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: hello
spec:
  params:
  - name: username
    type: string
  steps:
    - name: hello
      image: ubuntu
      script: |
        #!/bin/bash
        echo "Hello $(params.username)!"
```

A pipeline composes these tasks.

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: kueue-test
spec:
  params:
  - name: username
    type: string
  tasks:
    - name: sleep
      taskRef:
        name: sleep
    - name: hello
      runAfter:
        - sleep
      taskRef:
        name: hello
      params:
      - name: username
        value: $(params.username)
```

## a. Targeting a single LocalQueue

If you want every task to target a single [local queue](/docs/concepts/local_queue),
it should be specified in the `metadata.label` section of the PipelineRun configuration.

```yaml
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  generateName: kueue-test
  labels:
    kueue.x-k8s.io/queue-name: my-local-queue
spec:
  pipelineRef:
    name: kueue-test
  params:
  - name: username
    value: "Tekton"
```

This will inject the kueue label on every pod of the pipeline. Kueue will gate the pods once you are over the quota limits.

## c. How it works

Tekton pipelines manages its own pods, and only creates those pods once task nodes are
ready to execute. When configured, Kueue attaches an admission webhook that monitors for
pods created by Tekton pipelines. When it finds a newly created pod, it will add an
entry to the `spec.schedulingGates` parameter of the pod, preventing the Kubernetes scheduler
from assigning a node to the pod. It also creates a corresponding `Workload` resource to
track the resource requirements. Once the Workload meets all admission criteria,
Kueue will remove the scheduling gate and allow the pod to proceed.

Once the pod is scheduled and runs successfully, Tekton will register the task complete and continue with it's processing.

## d. Limitations

- Kueue will only manage pods created by Tekton.
- Each pod in a Workflow will create a new Workload resource and must wait for admission by Kueue.
- There is no way to ensure that a Workflow will complete before it is started. If one step of a multi-step Workflow does not have
available quota, Tekton pipelines will run all previous steps and then wait for quota to become available.
