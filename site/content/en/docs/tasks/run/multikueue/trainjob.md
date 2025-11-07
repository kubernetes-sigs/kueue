---
title: "Run TrainJobs in Multi-Cluster"
linkTitle: "TrainJob"
weight: 3
date: 2025-11-06
description: >
  Run a MultiKueue scheduled TrainJob from Kubeflow Trainer v2.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.11.0 and for Kubeflow Trainer at least v2.0.0.

See [Trainer Installation](https://www.kubeflow.org/docs/components/trainer/operator-guides/installation/) for installation and configuration details of Kubeflow Trainer v2.

{{% alert title="Note" color="primary" %}}
TrainJob is part of Kubeflow Trainer v2, which introduces a unified API for all training frameworks. It replaces the framework-specific job types (PyTorchJob, TFJob, etc.) from Trainer v1.

For legacy Kubeflow Jobs (v1), see [Run Kubeflow Jobs in Multi-Cluster](/docs/tasks/run/multikueue/kubeflow/).
{{% /alert %}}

## MultiKueue integration

Once the setup is complete you can test it by running a TrainJob [`sample-trainjob`](/docs/tasks/run/trainjobs/#using-clustertrainingruntime). 

{{% alert title="Note" color="primary" %}}
Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for TrainJob. 

This allows the Trainer to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip Pod creation. 

The pods are created and the actual computation will happen on the mirror copy of the TrainJob on the selected worker cluster. 
The mirror copy of the TrainJob does not have the field set.
{{% /alert %}}

## ClusterTrainingRuntime and TrainingRuntime

TrainJobs in MultiKueue environments work with both ClusterTrainingRuntime and TrainingRuntime:

- **ClusterTrainingRuntime**: Must be installed on all worker clusters. The runtime definition should be consistent across clusters.
- **TrainingRuntime**: Must be installed in the corresponding namespaces on the worker clusters where TrainJobs will run.

Ensure that the runtime configurations referenced by your TrainJobs are available on the target worker clusters before dispatching the jobs.

## Example

Here's a complete example of running a TrainJob with MultiKueue:

1. **Create a ClusterTrainingRuntime** on all clusters (management and workers):

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

2. **Create a MultiKueue-enabled TrainJob** on the management cluster:

```yaml
apiVersion: trainer.kubeflow.org/v1alpha1
kind: TrainJob
metadata:
  name: pytorch-multikueue
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: multikueue-queue
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

The TrainJob will be dispatched to a worker cluster based on the MultiKueue configuration and available resources. The training pods will be created and executed on the selected worker cluster.
