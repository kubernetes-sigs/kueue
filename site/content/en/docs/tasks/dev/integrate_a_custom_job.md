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
   - You will need to add a `+kubebuilder:rbac` directive for your CRD so Kueue will be permitted to manage it.
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

### Developing a mutation and validation webhook

Once you have implemented the `GenericJob` interface, you can register a webhook for the type by using
the `BaseWebhookFactory` in `pkg/controller/jobframework`. This webhook ensures that a job is created
in a suspended state and that queue names don't change when the job is admitted. You can read the
[MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob) integration as an example.

For certain use cases, you might need to register a dedicated webhook. In this case, you should use
Kueue's `LosslessDefaulter`, which ensures that the defaulter is compatible with future versions of the
CRD, under the assumption that the defaulter does **not** remove any fields from the original object.
You can read the [BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
integration as an example for how to use a `LosslessDefaulter`.

If the suggestions above are incompatible with your use case, beware of potential
[problems when using controller-runtime's CustomDefaulter](https://groups.google.com/a/kubernetes.io/g/wg-batch/c/WAaabCuGCoY).

### Adjust build system

Add required dependencies to compile your code. For example, using `go get github.com/kubeflow/mpi-operator@0.4.0`.

Update the [Makefile](https://github.com/kubernetes-sigs/kueue/blob/main/Makefile) for testing.
   - Add commands which copy the CRD of your custom job to the Kueue project.
   - Add your custom job operator CRD dependencies into `test-integration`.

## Building an External Integration

Building an external integration is similar to building a built-in integration, except that
you will instantiate Kueue's `jobframework` for your CRD in a separate executable.  The only
change you will make to the primary Kueue manager is a deployment-time configuration change
to enable it to recognize that your CRD is being managed by a Kueue-compatible manager.

Here are completed external integrations you can learn from:
   - [AppWrapper](https://github.com/project-codeflare/appwrapper)

### Registration

Configure Kueue by adding your framework's GroupVersionKind to `.integrations.externalFrameworks` in [controller_manager_config.yaml](
https://kueue.sigs.k8s.io/docs/installation/#install-a-custom-configured-released-version).

### Job Framework

Make the following changes to an existing controller-runtime based manager for your CRD.

Add a dependency on Kueue to your `go.mod` so you can import `jobframework` as a go library.

In a new `mycrd_workload_controller.go` file, implement the `GenericJob` interface, and other optional [interfaces defined by the framework](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go).

Extend the existing webhook for your CRD to invoke Kueue's webhook helper methods:
   - Your defaulter should invoke `jobframework.ApplyDefaultForSuspend` in [defaults.go](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/defaults.go)
   - Your validator should invoke `jobframework.ValidateJobOnCreate` and `jobframework.ValidateJobOnUpdate` in [validation.go](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/validation.go)

Extend your manager's startup procedure to do the following:
   - Using the `jobframework.NewGenericReconcilerFactory` method, create an instance of Kueue's JobReconciler
     for your CRD and register it with the controller-runtime manager.
   - Invoke `jobframework.SetupWorkloadOwnerIndex` to create an indexer for Workloads owned by your CRD.

Extend your existing RBAC Authorizations to include the privileges needed by
Kueue's JobReconciler:
```go
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

```

For a concrete example, consult these pieces of the AppWrapper controller:
   - [workload_controller.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/controller/workload/workload_controller.go)
   - [appwrapper_webhook.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/webhook/appwrapper_webhook.go)
   - [setup.go](https://github.com/project-codeflare/appwrapper/blob/main/pkg/controller/setup.go)
