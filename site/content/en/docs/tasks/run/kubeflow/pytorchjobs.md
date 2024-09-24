---
title: "Run a PyTorchJob"
date: 2023-08-09
weight: 6
description: >
  Run a Kueue scheduled PyTorchJob
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Training Operator](https://www.kubeflow.org/docs/components/training/pytorch/) PyTorchJobs.

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.

Check [the Training Operator installation guide](https://github.com/kubeflow/training-operator#installation).

Note that the minimum requirement training-operator version is v1.7.0.

You can [modify kueue configurations from installed releases](/docs/installation#install-a-custom-configured-released-version) to include PyTorchJobs as an allowed workload.

{{% alert title="Note" color="primary" %}}
In order to use Training Operator, prior to v0.8.1, you need to restart Kueue after the installation.
You can do it by running: `kubectl delete pods -lcontrol-plane=controller-manager -nkueue-system`.
{{% /alert %}}

## PyTorchJob definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the PyTorchJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Optionally set Suspend field in PyTorchJobs

```yaml
spec:
  runPolicy:
    suspend: true
```

By default, Kueue will set `suspend` to true via webhook and unsuspend it when the PyTorchJob is admitted.

## Sample PyTorchJob

This example is based on https://github.com/kubeflow/training-operator/blob/855e0960668b34992ba4e1fd5914a08a3362cfb1/examples/pytorch/simple.yaml.

{{< include "examples/jobs/sample-pytorchjob.yaml" "yaml" >}}


## MultiKueue integration

The same KubeflowJob example from [single cluster environment](#single-cluster-environment) can be used also in [MultiKueue environment](#multikueue-environment).

### JobManagedBy feature
{{% alert title="Note" color="primary" %}}
You can only set the `spec.runPolicy.managedBy` field on KubeflowJobs if you enable the JobManagedBy [feature gate](/docs/installation/#change-the-feature-gates-configuration) (disabled by default).
{{% /alert %}}

The feature allows you to disable the Kubeflow training-operator, for a specific KubeflowJob type, and delegate reconciliation of that job to the Kueue controller.
By default the `spec.runPolicy.managedBy` is nil, which translates to `kubeflow.org/training-operator` indicating the standard Kubeflow controller.

In order to change the controller that reconciles the job to the Kueue you need to set a value of that field to `kueue.x-k8s.io/multikueue`. The value of the field is immutable.

{{% alert title="Note" color="primary" %}}
The use of the feature is limited only to latest version of [training-operator](https://github.com/kubeflow/training-operator).
None of the released version of the training-operator supports the JobManagedBy feature.

In order to install latest version run:
```bash
kubectl apply -k "github.com/kubeflow/training-operator.git/manifests/overlays/standalone?ref=master"
```
For more details see [this](https://www.kubeflow.org/docs/components/training/installation/#installing-the-training-operator)
{{% /alert %}}




