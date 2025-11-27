---
title: "Run A TrainJob"
linkTitle: "TrainJobs"
date: 2024-11-05
weight: 6
description: >
  Run a Kueue scheduled TrainJob from Kubeflow Trainer v2
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Kubeflow Trainer](https://www.kubeflow.org/docs/components/trainer/) TrainJobs.

Kubeflow Trainer v2 introduces the `TrainJob` API that works seamlessly with Kueue for batch scheduling and resource management. TrainJobs can be configured to use either:

- **ClusterTrainingRuntime**: Cluster-scoped training runtimes that can be used across all namespaces
- **TrainingRuntime**: Namespace-scoped training runtimes that are only available within a specific namespace

Kueue manages TrainJobs by scheduling their underlying jobs according to available quota and priority.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

2. Install Kubeflow Trainer v2. Check [the Trainer installation guide](https://www.kubeflow.org/docs/components/trainer/operator-guides/installation/).

   **Note**: The minimum required Trainer version is v2.0.0.

3. Enable TrainJob integration in Kueue. You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include TrainJobs as an allowed workload.

## TrainJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the TrainJob configuration:

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Suspend field

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the TrainJob is admitted:

```yaml
spec:
  suspend: true
```

## Using ClusterTrainingRuntime

ClusterTrainingRuntimes are cluster-scoped resources that define training configurations accessible across all namespaces. They are typically created by platform administrators. For more details on ClusterTrainingRuntime configuration, see the [Kubeflow Runtime Guide](https://www.kubeflow.org/docs/components/trainer/operator-guides/runtime/).

### Example: PyTorch Distributed Training with ClusterTrainingRuntime

First, create a ClusterTrainingRuntime for PyTorch distributed training:

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: ClusterTrainingRuntime
metadata:
  name: torch-distributed
  labels:
    trainer.kubeflow.org/framework: torch
spec:
  mlPolicy:
    numNodes: 1
    torch:
      numProcPerNode: auto
  template:
    spec:
      replicatedJobs:
        - name: node
          template:
            metadata:
              labels:
                trainer.kubeflow.org/trainjob-ancestor-step: trainer
            spec:
              template:
                spec:
                  containers:
                    - name: node
                      image: pytorch/pytorch:2.7.1-cuda12.8-cudnn9-runtime
```

Now, create a TrainJob that references this ClusterTrainingRuntime and will be scheduled by Kueue:

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: TrainJob
metadata:
  name: pytorch-distributed
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  runtimeRef:
    name: torch-distributed
    kind: ClusterTrainingRuntime
  trainer:
    numNodes: 2
    resourcesPerNode:
      requests:
        cpu: "4"
        memory: "8Gi"
        nvidia.com/gpu: "1"
```

**Key Points:**
- The `kueue.x-k8s.io/queue-name` label is the only Kueue-specific addition needed
- The `runtimeRef` points to the `ClusterTrainingRuntime` named `torch-distributed`
- This example uses the default executable image from the runtime definition
- Kueue will manage the lifecycle and admission of this TrainJob based on available quota
- For custom training code, see the [Kubeflow Trainer examples](https://github.com/kubeflow/trainer/tree/master/examples)

## Using TrainingRuntime (Namespace-scoped)

TrainingRuntimes are namespace-scoped resources that provide more granular control per namespace. They are useful when different teams need customized training configurations.

### Example: Custom PyTorch Training with TrainingRuntime

Create a namespace-scoped TrainingRuntime:

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: TrainingRuntime
metadata:
  name: torch-custom
  namespace: team-a
spec:
  mlPolicy:
    numNodes: 1
    torch:
      numProcPerNode: auto
  template:
    spec:
      replicatedJobs:
        - name: node
          template:
            metadata:
              labels:
                trainer.kubeflow.org/trainjob-ancestor-step: trainer
            spec:
              template:
                spec:
                  containers:
                    - name: trainer
                      image: pytorch/pytorch:2.7.1-cuda12.8-cudnn9-runtime
                      env:
                        - name: CUSTOM_ENV
                          value: "team-a-value"
```

Create a TrainJob that uses this namespace-scoped runtime:

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: TrainJob
metadata:
  name: pytorch-custom
  namespace: team-a
  labels:
    kueue.x-k8s.io/queue-name: team-a-queue
spec:
  runtimeRef:
    name: torch-custom
    kind: TrainingRuntime
    apiGroup: trainer.kubeflow.org
  trainer:
    image: docker.io/team-a/custom-training:latest
    numNodes: 1
    resourcesPerNode:
      requests:
        cpu: "2"
        memory: "4Gi"
```

**Key Points:**
- The TrainingRuntime is created in the same namespace as the TrainJob (`team-a`)
- The `runtimeRef` specifies `kind: TrainingRuntime` to use the namespace-scoped runtime
- Each namespace can have its own customized runtimes with different configurations

## Using Workload Priority

To prioritize TrainJobs, use Kueue's workload priority classes. See [Run job with WorkloadPriority](/docs/tasks/manage/run_job_with_workload_priority) for details on configuring and using workload priority classes.

TrainJobs use the same priority mechanism as other Kueue workloads via the `kueue.x-k8s.io/priority-class` label.

## LLM Fine-Tuning with Kueue

Kubeflow Trainer v2 supports LLM fine-tuning with TorchTune and DeepSpeed. For comprehensive examples, see:

- [Fine-tune Llama-3.2-1B with Alpaca Dataset](https://github.com/kubeflow/trainer/blob/master/examples/torchtune/llama3_2/alpaca-trainjob-yaml.ipynb)
- [Fine-tune Qwen2.5-1.5B with Alpaca Dataset](https://github.com/kubeflow/trainer/blob/master/examples/torchtune/qwen2_5/qwen2.5-1.5B-with-alpaca.ipynb)
- [T5 Fine-Tuning with DeepSpeed](https://github.com/kubeflow/trainer/blob/master/examples/deepspeed/text-summarization/T5-Fine-Tuning.ipynb)

To use Kueue scheduling with these examples, add the queue label to your TrainJob:

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: gpu-queue  # Add this label for Kueue scheduling
spec:
  runtimeRef:
    name: torchtune-llama3.2-1b
    kind: ClusterTrainingRuntime
  # ... rest of the TrainJob spec as shown in Kubeflow examples
```

## Differences from Kubeflow Training Operator V1

{{% alert title="Important" color="warning" %}}
Kubeflow Trainer v2 introduces a new API that is not compatible with Training Operator v1 APIs (PyTorchJob, TFJob, etc.). The key differences are:

- **Unified API**: TrainJob replaces framework-specific CRDs like PyTorchJob, TFJob
- **Runtime-based**: Training configurations are defined in reusable Runtimes
- **Built on JobSet**: Uses Kubernetes JobSet as the underlying infrastructure
- **Better integration**: Native support for Kueue scheduling from the start

For migration guidance, refer to the [Kubeflow Trainer documentation](https://www.kubeflow.org/docs/components/trainer/operator-guides/migration/).
{{% /alert %}}

## Best Practices

1. **Use ClusterTrainingRuntimes for common patterns**: Create cluster-scoped runtimes for frequently used training configurations
2. **Use TrainingRuntimes for team-specific needs**: Leverage namespace-scoped runtimes for customizations per team
3. **Set appropriate resource requests**: Ensure your TrainJob resource requests match the ResourceFlavor in your ClusterQueue
4. **Monitor quota usage**: Use `kubectl get clusterqueue` to track resource utilization
5. **Use priority classes**: Assign priorities to TrainJobs to ensure critical workloads are scheduled first
6. **Test with small configurations**: Before scaling up, test your TrainJob configuration with minimal resources

## Additional Resources

- [Kubeflow Trainer Documentation](https://www.kubeflow.org/docs/components/trainer/)
- [Kubeflow Runtime Guide](https://www.kubeflow.org/docs/components/trainer/operator-guides/runtime/)
- [Fine-tune LLMs with TorchTune](https://www.kubeflow.org/docs/components/trainer/user-guides/builtin-trainer/torchtune/)
- [Kueue Concepts](/docs/concepts/)
- [Run job with WorkloadPriority](/docs/tasks/manage/run_job_with_workload_priority)
- [Monitor Pending Workloads](/docs/tasks/manage/monitor_pending_workloads)
- [Troubleshooting](/docs/tasks/troubleshooting)
- [Kubeflow Python SDK](https://github.com/kubeflow/sdk/)

## Troubleshooting

For general troubleshooting guidance, see the [Kueue troubleshooting guide](/docs/tasks/troubleshooting).

For TrainJob-specific issues, verify that the referenced ClusterTrainingRuntime or TrainingRuntime exists:

```bash
kubectl get clustertrainingruntime
kubectl get trainingruntime -n <namespace>
```
