---
title: "Integrate a custom Job with Kueue"
date: 2023-07-25
weight: 8
description: >
  Integrate a custom Job with Kueue.
---

Kueue integrates with a couple of Jobs, including Kubernetes batch Job, MPIJob, RayJob or JobSet.  

There are two options for integrating a Job-like CRD with Kueue:
- As part of the Kueue repository
- Writing an external controller

This guide is for [platform developers](/docs/tasks#platform-developer) and focuses on the first approach.
If there is a widely used Job which you would like supported in Kueue, please consider contributing.
This page shows how to integrate a custom Job with Kueue.

## Pre-requisites

1. The custom Job CRD should have a suspend-like field, with semantics similar to the [`suspend` field in a Kubernetes Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job).

## What you need to specify

1. Configuration
2. Controller
3. Webhook
4. Adjust build system

## Configuration

Add your framework name to `.integrations.frameworks` in [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)

## Controller

Add a new folder in `./pkg/controller/jobs/` to host the implementation of the integration.

1. Register the new framework in `func Init()`
2. Add RBAC Authorization using [kubebuilder marker comments](https://book.kubebuilder.io/reference/markers/rbac.html)
    - Add RBAC Authorization for priorityclasses, events, workloads, resourceflavors and any resources you want.
3. Implement the `GenericJob` interface, and other optional [interfaces defined by the framework](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go).

## Webhook

Create a webhook file in the dedicated directory for the integration.

You can learn how to create webhook in this [page](https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html).

The framework provides some validation functions for common labels and annotations.

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