---
title: "Run An Argo Workflow"
date: 2024-01-03
weight: 7
description: >
  Integrate Kueue with Argo Workflows.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Argo Workflows](https://argo-workflows.readthedocs.io/en/latest/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

As of the writing of this doc, Kueue doesn't support Argo Workflows [Workflow](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/) resources directly,
but you can take advantage of the ability for Kueue to [manage plain pods](/docs/tasks/run_plain_pods) to integrate them.

1. By default, the integration for `v1/pod` is not enabled.
   Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
   and enable the `pod` integration.

   The pod integration for Argo Workflows looks like:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta1
   kind: Configuration
   integrations:
     frameworks:
      - "pod"
     podOptions:
       # You can change namespaceSelector to define in which
       # namespaces kueue will manage the Workflow pods.
       namespaceSelector:
         matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: NotIn
           values: [ kube-system, kueue-system ]
       # Argo Workflows uses the workflows.argoproj.io/completed label to
       # keep track of pods it manages. We will use that as a hint for Kueue
       # to find Argo Workflow pods.
       podSelector:
         matchExpressions:
         - key: workflows.argoproj.io/completed
           operator: In
           values: [ "false" ]
   ```

2. Pods that belong to other API resources managed by Kueue are excluded from being queued by `pod` integration.
   For example, pods managed by `batch/v1.Job` won't be managed by `pod` integration.

4. Check [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial Kueue setup.

## Workflow definition

When creating [Workflow](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/),
[WorkflowTemplate](https://argo-workflows.readthedocs.io/en/latest/workflow-templates/),
[ClusterWorkflowTemplate](https://argo-workflows.readthedocs.io/en/latest/cluster-workflow-templates/),
or [CronWorkflow](https://argo-workflows.readthedocs.io/en/latest/cron-workflows/) resources for
Kueue, take into consideration the following aspects:

### a. Targeting a single LocalQueue

If you want the entire workflow to target a single [local queue](/docs/concepts/local_queue),
it should be specified in the `spec.podMetadata` section of the Workflow configuration.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: hello-world-
spec:
  entrypoint: whalesay
  podMetadata:
    labels:
      kueue.x-k8s.io/queue-name: user-queue # All pods will target user-queue
  templates:
    - name: whalesay
      container:
        image: docker/whalesay
        command: [ cowsay ]
        args: [ "hello world" ]
        resources:
          limits:
            memory: 32Mi
            cpu: 100m
```

### b. Targeting a different LocalQueue per template

If prefer to target a different [local queue](/docs/concepts/local_queue) for each step of your Workflow,
you can define the queue in the `spec.templates[].metadata` section of the Workflow configuration.

In this example `hello1` and `hello2a` will target `user-queue` and `hello2b` will
target `user-queue-2`.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: steps-
spec:
  entrypoint: hello-hello-hello

  templates:
  - name: hello-hello-hello
    steps:
    - - name: hello1            # hello1 is run before the following steps
        template: whalesay
        arguments:
          parameters:
          - name: message
            value: "hello1"
    - - name: hello2a           # double dash => run after previous step
        template: whalesay
        arguments:
          parameters:
          - name: message
            value: "hello2a"
      - name: hello2b           # single dash => run in parallel with previous step
        template: whalesay-queue-2
        arguments:
          parameters:
          - name: message
            value: "hello2b"

  - name: whalesay
    metadata:
      labels:
        kueue.x-k8s.io/queue-name: user-queue # Pods from this template will target user-queue
    inputs:
      parameters:
      - name: message
    container:
      image: docker/whalesay
      command: [cowsay]
      args: ["{{inputs.parameters.message}}"]

  - name: whalesay-queue-2
    metadata:
      labels:
        kueue.x-k8s.io/queue-name: user-queue-2 # Pods from this template will target user-queue-2
    inputs:
      parameters:
      - name: message
    container:
      image: docker/whalesay
      command: [cowsay]
      args: ["{{inputs.parameters.message}}"]
```

### c. How it works

Argo Workflows managed its own pods, and only creates those pods once workflow nodes are
ready to execute. When configured, Kueue attaches an admission webhook that monitors for
pods created by Argo Workflows. When it finds a newly created pod, it will add an
entry to the `spec.schedulingGates` parameter of the pod, preventing the Kubernetes scheduler
from assigning a node to the pod. It also creates a corresponding `Workload` resource to
track the resource requirements. Once the Workload meets all admission criteria,
Kueue will remove the scheduling gate and allow the pod to proceed.

Once the pod is scheduled and runs successfully, Argo Workflows will register the node complete and continue with it's processing.

### d. Limitations

- Kueue will only manage pods created by Argo Workflows. It does not manage the Argo Workflows resources in any way.
- Each pod in a Workflow will create a new Workload resource and must wait for admission by Kueue.
- There is no way to ensure that a Workflow will complete before it is started. If one step of a multi-step Workflow does not have
available quota, Argo Workflows will run all previous steps and then wait for quota to become available.
- Kueue does not understand Argo Workflows `suspend` flag and will not manage it.
- Kueue does not manage `suspend`, `http`, or `resource` template types since they do not create pods.
