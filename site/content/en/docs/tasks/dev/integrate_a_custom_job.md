---
title: "Integrate a custom Job with Kueue"
date: 2023-07-25
weight: 8
description: >
  Integrate a custom Job with Kueue.
---

Kueue has built-in integrations for several Job types, including
Kubernetes batch Job, MPIJob, RayJob and JobSet.

There are two options for adding an additional integration for a Job-like CRD with Kueue:
- As part of the Kueue repository
- Writing an external controller

This guide is for [platform developers](/docs/tasks#platform-developer) and describes how
to build a new integration. Integrations should be built using the APIs provided by
Kueue's `jobframework` package. This will both simplify development and ensure that
your controller will be properly structured to become a core built-in integration if your
Job type is widely used by the community.

## Overview of Requirements

Kueue uses the [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).
We recommend becoming familiar with it and with
[Kubebuilder](https://github.com/kubernetes-sigs/kubebuilder) before starting to build a Kueue integration.

Whether you are building an external or built-in integration, your main tasks are
the following:

1. To work with Kueue, your custom Job CRD should have a suspend-like
field, with semantics similar to the [`suspend` field in a Kubernetes
Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job).
The field needs to be in your CRD's `spec`, not its `status`, to enable its
value to be set from a webhook. Your CRD's primary controller must respond to
changes in the value of this field by suspending or unsuspending its owned resources.

2. You will need to register the GroupVersionKind of your CRD with Kueue as
an integration.

3. You will need to instantiate various pieces of Kueue's `jobframework` package for
your CRD:
   - You will need to implement Kueue's `GenericJob` interface for your CRD
   - You will need to instantiate a `ReconcilerFactory` and register it with the controller runtime.
   - You will need to register webhooks that set the initial value of the `suspend` field in instances
     of your CRD and validate Kueue invariants on creation and update operations.
   - You will need to instantiate a `Workload` indexer for your CRD.

## Building a Built-in Integration

To get started, add a new folder in `./pkg/controller/jobs/` to host the implementation of the integration.

Here are completed built-in integrations you can learn from:
   - [BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
   - [JobSet](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/jobset)
   - [MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob)
   - [RayJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/rayjob)

### Registration

Add your framework name to `.integrations.frameworks` in [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)

Add RBAC Authorization for your CRD using [kubebuilder marker comments](https://book.kubebuilder.io/reference/markers/rbac.html).

Write a go `func init()` that invokes the jobframework `RegisterIntegration()` function.

### Job Framework

In the `mycrd_controller.go` file in your folder, implement the `GenericJob` interface, and other optional [interfaces defined by the framework](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go).

In the `mycrd_webhook.go` file in your folder, provide the webhook that invokes helper methods in from the jobframework to
set the initial suspend status of created jobs and validate invariants.

Add testing files for both the controller and the webhook.  You can check the test files in the other subfolders of `./pkg/controller/jobs/`
to learn how to implement them.

### Adjust build system

Add required dependencies to compile your code. For example, using `go get github.com/kubeflow/mpi-operator@0.4.0`.

Update the [Makefile](https://github.com/kubernetes-sigs/kueue/blob/main/Makefile) for testing.
   - Add commands which copy the CRD of your custom job to the Kueue project.
   - Add your custom job operator CRD dependencies into `test-integration`.

## Building an External Integration

Here are completed external integrations you can learn from:
   - [AppWrapper](https://github.com/project-codeflare/appwrapper)

### Registration

Add your framework's GroupVersionKind to `.integrations.externalFrameworks` in [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)

### Job Framework

Add a dependency on Kueue to your `go.mod`, import the `jobframework` and use it as described above to
create your controller and webhook implementations. In the `main` function of your controller, instantiate the controller-runtime manager
and register your webhook, indexer, and controller.

For a concrete example, consult these pieces of the AppWrapper controller:
   - [workload_controller.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/controller/workload/workload_controller.go)
   - [appwrapper_webhook.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/webhook/appwrapper_webhook.go)
   - [setup.go](https://github.com/project-codeflare/appwrapper/blob/main/pkg/controller/setup.go)
