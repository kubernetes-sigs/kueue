---
title: "Integrate a custom Job"
date: 2022-07-25
weight: 8
description: >
  Integrate a custom Job with Kueue.
---
# Integrate a custom Job with Kueue

This page shows how to integrate a custom Job with Kueue. 

## What we need to build

1. Configration
2. Controller
3. Webhook

## Configure your custom Job

Add your framework name to `.integrations.frameworks` in `config/components/manager/controller_manager_config.yaml`

## Build controller

You can create the controller in `./pkg/controller/jobs/$YOUR_CUSTOM_JOB`.

1. Register a new framework
2. Add RBAC Authorization
    - Add RBAC Authorization for priorityclasses, events, workloads, resourceflavors and any resources you want.
3. Implement interfaces
    - `Object()`: returns the job instance.
    - `IsSuspended()`: returns whether the job is suspended or not.
    - `Suspend()`: will suspend the job.
    - `RunWithPodSetsInfo(nodeSelectors []PodSetInfo)`: will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
    - `RestorePodSetsInfo(nodeSelectors []PodSetInfo)`: will restore the original node affinity and podSet counts of the job.
    - `Finished()`: means whether the job is completed/failed or not, condition represents the workload finished condition.
    - `PodSets()`: will build workload podSets corresponding to the job.
    - `IsActive()`: returns true if there are any running pods.
    - `PodsReady()`: instructs whether job derived pods are all ready now.
    - `GetGVK()`: returns GVK (Group Version Kind) for the job.

## Build webhook

You can create the webhook in `./pkg/controller/jobs/$YOUR_CUSTOM_JOB`.

You can learn how to create webhook in this [page](https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html). Also, you can see webhook [example](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobs/rayjob/rayjob_webhook.go) of RayJob with Kueue.

Make sure that your custom job has **suspend** abilities. Kueue will only schedules suspended job.