---
title: "Integrate a custom Job"
date: 2023-07-25
weight: 8
description: >
  Integrate a custom Job with Kueue.
---
# Integrate a custom Job with Kueue

Kueue integrates with a couple of jobs, including Kubernetes batch Job, MPIJob, RayJob or JobSet.  
If there is another Job, which you would like supported, please consider contributing. 
This page shows how to integrate a custom Job with Kueue.

## What you need to specify

1. Configuration
2. Controller
3. Webhook
4. Adjust build system

## Configuration

Add your framework name to `.integrations.frameworks` in [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)

## Controller

Provide your code of the controller under a dedicated directory. This directory need to under the `./pkg/controller/jobs/` folder.

1. Register the new framework
2. Add RBAC Authorization using [kubebuilder marker comments](https://book.kubebuilder.io/reference/markers/rbac.html)
    - Add RBAC Authorization for priorityclasses, events, workloads, resourceflavors and any resources you want.
3. Implement the `GenericJob` interface, and potentially other interfaces in [here](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go).
    - You can click this [link](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go) to check which interfaces you need to implement.


## Webhook

Create your webhook file in your dedicated director.

You can learn how to create webhook in this [page](https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html).

Your webhook should have ability to suspend jobs.


## Adjust build system
1. Add required dependencies to compile the controller and webhook code. For example, using `go get github.com/kubeflow/mpi-operator@0.4.0`.
2. Update [Makefile](https://github.com/kubernetes-sigs/kueue/blob/main/Makefile) for testing.
   - Add commands which copy the crd of your custom job to the Kueue project.
   - Add your custom job operator crd dependencies into `test-integration`.

   For testing files, you can check the sample test files in [completed integrations](#completed-integrations) to learn how to implement them.

## Completed integrations
Here are completed integrations you can learn from:
   - [BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
   - [JobSet](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/jobset)
   - [MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob)
   - [RayJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/rayjob)