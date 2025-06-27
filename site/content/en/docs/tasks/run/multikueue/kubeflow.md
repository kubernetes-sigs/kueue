---
title: "Run Kubeflow Jobs in Multi-Cluster"
linkTitle: "Kubeflow"
weight: 4
date: 2024-09-25
description: >
  Run a MultiKueue scheduled Kubeflow Jobs.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.11.0 and for Kubeflow Trainer at least v1.9.0.

See [Trainer Installation](https://www.kubeflow.org/docs/components/training/installation/#installing-the-training-operator) for installation and configuration details of Trainer.

{{% alert title="Note" color="primary" %}}
Before the [ManagedBy feature](https://github.com/kubeflow/trainer/issues/2193) was supported in Kueue (below v0.11.0), the installation of Kubeflow Trainer in the <b>Manager Cluster</b> must be limited to CRDs only.

To install the CRDs run:
```bash
kubectl apply -k "github.com/kubeflow/trainer.git/manifests/base/crds?ref=v1.9.0"
```
{{% /alert %}}

## MultiKueue integration

Once the setup is complete you can test it by running one of the Kubeflow Jobs e.g. PyTorchJob [`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob). 

{{% alert title="Note" color="primary" %}}
Kueue defaults the `spec.runPolicy.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for all Kubeflow Jobs. 

This allows the Trainer to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip Pod creation. 

The pods are created and the actual computation will happen on the mirror copy of the Job on the selected worker cluster. 
The mirror copy of the Job does not have the field set.
{{% /alert %}}

## Working alongside MPI Operator
In order for MPI-operator and Trainer to work on the same cluster it is required that:
1. `kubeflow.org_mpijobs.yaml` entry is removed from `base/crds/kustomization.yaml` - https://github.com/kubeflow/trainer/issues/1930
2. Trainer deployment is modified to enable all kubeflow jobs except for MPI -  https://github.com/kubeflow/trainer/issues/1777
  