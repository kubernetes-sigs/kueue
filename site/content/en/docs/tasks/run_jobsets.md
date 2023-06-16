---
title: "Run A JobSet"
date: 2023-06-16
weight: 7
description: >
  Run a Kueue scheduled JobSet.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [JobSet Operator](https://github.com/kubernetes-sigs/jobset) [JobSets](https://github.com/kubernetes-sigs/jobset/blob/main/docs/concepts/README.md).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. By default, the integration for `jobset.x-k8s.io/jobset` is not enabled.
  Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version). `jobset.x-k8s.io/jobset` integration should be specified in the `integrations.frameworks` section of the custom manager configuration.

```yaml
integrations:
  frameworks:
  - "jobset.x-k8s.io/jobset"
```

2. Check [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [JobSet Installation](https://github.com/kubernetes-sigs/jobset/blob/main/docs/setup/install.md) for installation and configuration details of JobSet Operator.

## JobSet definition

When running [JobSets](https://github.com/kubernetes-sigs/jobset/blob/main/docs/concepts/README.md) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the JobSet configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.replicatedJobs`.

```yaml
      - template:
          spec:
            template:
              spec:
                containers:
                  - resources:
                      requests:
                        cpu: 1
```

## Example JobSet

The JobSet looks like the following:

```yaml
# jobset-sample.yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: sleep
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicatedJobs:
    - name: workers
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

You can run this JobSet with the following commands:

```sh
# Create the JobSet (once)
kubectl apply -f jobset-sample.yaml
# To observe the queueing and admission of the jobs you can clone this example with different metadata.name and apply them.
kubectl apply -f jobset-job-sample-colone-1.yaml
kubectl apply -f jobset-job-sample-colone-2.yaml
```
