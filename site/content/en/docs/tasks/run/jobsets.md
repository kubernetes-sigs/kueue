---
title: "Run A JobSet"
linkTitle: "Jobsets"
date: 2023-06-16
weight: 7
description: >
  Run a Kueue scheduled JobSet.
---

This document explains how you can use Kueue’s scheduling and resource management functionality when running [JobSet Operator](https://github.com/kubernetes-sigs/jobset) [JobSet](https://jobset.sigs.k8s.io/docs/concepts/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

2. See [JobSet Installation](https://jobset.sigs.k8s.io/docs/installation/) for installation and configuration details of JobSet Operator.

## JobSet definition

When running [JobSets](https://jobset.sigs.k8s.io/docs/concepts/) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the JobSet configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. MultiKueue

If the JobSet is submitted to a queue using [MultiKueue](/docs/concepts/multikueue) its `spec.managedBy` field needs to be set to `kueue.x-k8s.io/multikueue`. Otherwise the its workload will be marked as `Finished` with an error indicating this cause.

### c. Configure the resource needs

The resource needs of the workload can be configured in the `spec.replicatedJobs`. Should also be taken into account that number of replicas, [parallelism](https://kubernetes.io/docs/concepts/workloads/controllers/job/#parallel-jobs) and completions affect the resource calculations. 

```yaml
    - replicas: 1
      template:
        spec:
          completions: 2
          parallelism: 2
          template:
            spec:
              containers:
                - resources:
                    requests:
                      cpu: 1
```

### d. Jobs prioritisation
  
The first [PriorityClassName](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#priorityclass) of `spec.replicatedJobs` that is not empty will be used as the priority.

```yaml
    - template:
        spec:
          template:
            spec:
              priorityClassName: high-priority
```

## Example JobSet

The JobSet looks like the following:

### Single Cluster Environment

```yaml
# jobset-sample.yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  generateName: sleep-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  network:
    enableDNSHostnames: false
    subdomain: some-subdomain
  replicatedJobs:
    - name: workers
      replicas: 2
      template:
        spec:
          parallelism: 4
          completions: 4
          backoffLimit: 0
          template:
            spec:
              containers:
                - name: sleep
                  image: busybox
                  resources:
                    requests:
                      cpu: 1
                      memory: "200Mi"
                  command:
                    - sleep
                  args:
                    - 100s
    - name: driver
      template:
        spec:
          parallelism: 1
          completions: 1
          backoffLimit: 0
          template:
            spec:
              containers:
                - name: sleep
                  image: busybox
                  resources:
                    requests:
                      cpu: 2
                      memory: "200Mi"
                  command:
                    - sleep
                  args:
                    - 100s
```

### MultiKueue Environment

```yaml
# jobset-sample.yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  generateName: sleep-job-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  network:
    enableDNSHostnames: false
    subdomain: some-subdomain
  replicatedJobs:
    - name: workers
      replicas: 2
      template:
        spec:
          parallelism: 4
          completions: 4
          backoffLimit: 0
          template:
            spec:
              containers:
                - name: sleep
                  image: busybox
                  resources:
                    requests:
                      cpu: 1
                      memory: "200Mi"
                  command:
                    - sleep
                  args:
                    - 100s
    - name: driver
      template:
        spec:
          parallelism: 1
          completions: 1
          backoffLimit: 0
          template:
            spec:
              containers:
                - name: sleep
                  image: busybox
                  resources:
                    requests:
                      cpu: 2
                      memory: "200Mi"
                  command:
                    - sleep
                  args:
                    - 100s
  managedBy: kueue.x-k8s.io/multikueue
```

You can run this JobSet with the following commands:

```sh
# To monitor the queue and admission of the jobs, you can run this example multiple times:
kubectl create -f jobset-sample.yaml
```
