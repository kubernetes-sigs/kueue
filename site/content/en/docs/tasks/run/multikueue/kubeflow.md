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

For the ease of setup and use we recommend using at least Kueue v0.8.1 and for Kubeflow Training Operator at least v1.8.1.

### Manager Cluster

{{% alert title="Note" color="primary" %}}
Before the [ManagedBy feature](https://github.com/kubeflow/training-operator/issues/2193) will become a part of the release of Kubeflow Training Operator the installation of Kubeflow Training Operator in the manager cluster must be limited to CRDs only.
{{% /alert %}}

To install the CRDs run:
```bash
kubectl apply -k "github.com/kubeflow/training-operator.git/manifests/base/crds?ref=v1.8.0"
```

### Worker Cluster

See [Training Operator Installation](https://www.kubeflow.org/docs/components/training/installation/#installing-the-training-operator) for installation and configuration details of Training Operator.

## MultiKueue integration

Once the setup is complete you can test it by running one of the Kubeflow Jobs e.g. PyTorchJob [`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob). 


## Working alongside MPI Operator
In order for MPI-operator and Training-operator to work on the same cluster it is required that:
1. `kubeflow.org_mpijobs.yaml` entry is removed from `base/crds/kustomization.yaml` - https://github.com/kubeflow/training-operator/issues/1930
2. Training Operator deployment is modified to enable all kubeflow jobs except for MPI -  https://github.com/kubeflow/training-operator/issues/1777
  