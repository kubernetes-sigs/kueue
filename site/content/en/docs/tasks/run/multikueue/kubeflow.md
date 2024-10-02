---
title: "Run Kubeflow Jobs in Multi-Cluster"
linkTitle: "Kubeflow"
weight: 2
date: 2024-09-25
description: >
  Run a MultiKueue scheduled Kubeflow Jobs.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.8.1.

{{% alert title="Note" color="primary" %}}
The use of the feature is limited only to latest version of [Kubeflow Training Operator](https://github.com/kubeflow/training-operator).
None of the released version of the training-operator supports the JobManagedBy feature.

In order to install the least version containing the feature run:
```bash
kubectl apply -k "github.com/kubeflow/training-operator.git/manifests/overlays/standalone?ref=d8b8b347ae33cbfd32bf5df81797d2d715724e87"
```
For more details see [this](https://www.kubeflow.org/docs/components/training/installation/#installing-the-training-operator)
{{% /alert %}}

## MultiKueue integration

Once the setup is complete you can test it by running one of the Kubeflow Jobs e.g. PyTorchJob [`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob). 

### JobManagedBy feature
{{% alert title="Note" color="primary" %}}
You can only set the `spec.runPolicy.managedBy` field on KubeflowJobs if you enable the JobManagedBy [feature gate](/docs/installation/#change-the-feature-gates-configuration) (disabled by default).
{{% /alert %}}

The feature allows you to disable the Kubeflow training-operator, for a specific KubeflowJob type, and delegate reconciliation of that job to the Kueue controller.
By default the `spec.runPolicy.managedBy` is nil, which translates to `kubeflow.org/training-operator` indicating the standard Kubeflow Training Operator controller.

In order to change the controller that reconciles the job to the Kueue you need to set a value of that field to `kueue.x-k8s.io/multikueue`. The value of the field is immutable.