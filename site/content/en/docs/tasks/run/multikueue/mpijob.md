---
title: "Run MPI Jobs in Multi-Cluster"
linkTitle: "MPI"
weight: 3
date: 2024-10-25
description: >
  Run a MultiKueue scheduled MPI Jobs.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the proper setup and use it is required using at least Kueue v0.9.0 and for MPI Operator at least v0.6.0.

### Installation on the Clusters

{{% alert title="Note" color="primary" %}}
Note: While both MPI Operator and Training Operator must be running on the same cluster, there are special steps that has to be applied to Training Operator deployment.
See [Working alongside MPI Operator](/docs/tasks/run/multikueue/kubeflow#working-alongside-mpi-operator) for more details.
{{% /alert %}}

See [MPI Operator Installation](https://www.kubeflow.org/docs/components/training/user-guides/mpi/#installation) for installation and configuration details of MPI Operator.

## MultiKueue integration

Once the setup is complete you can test it by running a MPI Job [`sample-mpijob.yaml`](/docs/tasks/run/kubeflow/mpijobs/#sample-mpijob). 
