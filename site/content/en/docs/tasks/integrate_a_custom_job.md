---
title: "Integrate a custom Job"
date: 2023-07-25
weight: 8
description: >
  Integrate a custom Job with Kueue.
---
# Integrate a custom Job with Kueue

This page shows how to integrate a custom Job with Kueue. 

## What you need to specify

1. Configuration
2. Controller
3. Webhook
4. Test files
5. Adjust build system

## Configure your custom Job

Add your framework name to `.integrations.frameworks` in [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)

## Build controller

Provide your code of the controller under a dedicated directory. This directory need to under the `./pkg/controller/jobs/` folder.

1. Register a new framework
2. Add RBAC Authorization
    - Add RBAC Authorization for priorityclasses, events, workloads, resourceflavors and any resources you want.
3. Implement interfaces
    - You can click this [link](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go) to check which interfaces you need to implement.


## Build webhook

Create your webhook file in your dedicated director.

You can learn how to create webhook in this [page](https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html).

Make sure that your custom job has **suspend** abilities. Kueue will only schedule suspended job.

## Add test files
Create test files which help test your custom job's controller and webhook.

You can check the sample test files in [completed integrations](#completed-integrations) to learn how to implement them.

## Adjust build system
Update [Makefile](https://github.com/kubernetes-sigs/kueue/blob/main/Makefile) for testing.
   - Add commands which copy the crd of your custom job to the Kueue project.
   - Add your custom job operator crd dependencies into `test-integration`.

## Completed integrations
Here are 4 completed integrations. You can learn them as examples:
   - [BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
   - [JobSet](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/jobset)
   - [MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob)
   - [RayJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/rayjob)