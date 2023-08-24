---
title: "Run A RayJob"
date: 2023-05-18
weight: 6
description: >
  Run a Kueue scheduled RayJob.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [KubeRay's](https://ray-project.github.io/kuberay/)
[RayJob](https://ray-project.github.io/kuberay/guidance/rayjob/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. By default, the integration for `ray.io/rayjob` is not enabled.
  Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
  and enable the `ray.io/rayjob` integration.

2. Check [Administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial Kueue setup.

3. See [KubeRay Installation](https://ray-project.github.io/kuberay/deploy/installation/) for installation and configuration details of KubeRay.

## RayJob definition

When running [RayJobs](https://ray-project.github.io/kuberay/guidance/rayjob/) on
Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the RayJob configuration.

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.rayClusterSpec`.

```yaml
    headGroupSpec:
      template:
        spec:
          containers:
            - resources:
                requests:
                  cpu: "1"
    workerGroupSpecs:
      - template:
          spec:
            containers:
              - resources:
                  requests:
                    cpu: "1"
```

### c. Limitations

- A Kueue managed RayJob cannot use an existing RayCluster.
- The RayCluster should be deleted at the end of the job execution, `spec.ShutdownAfterJobFinishes` should be `true`.
- Because Kueue will reserve resources for the RayCluster, `spec.rayClusterSpec.enableInTreeAutoscaling` should be `false`.
- Because a Kueue workload can have a maximum of 8 PodSets, the maximum number of `spec.rayClusterSpec.workerGroupSpecs` is 7.

## Example RayJob

In this example, the code is provided to the Ray framework via a ConfigMap.

```yaml
# ray-job-code-sample.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ray-job-code-sample
data:
  sample_code.py: |
    import ray
    import os
    import requests

    ray.init()

    @ray.remote
    class Counter:
        def __init__(self):
            # Used to verify runtimeEnv
            self.name = os.getenv("counter_name")
            self.counter = 0

        def inc(self):
            self.counter += 1

        def get_counter(self):
            return "{} got {}".format(self.name, self.counter)

    counter = Counter.remote()

    for _ in range(5):
        ray.get(counter.inc.remote())
        print(ray.get(counter.get_counter.remote()))

    print(requests.__version__)
```

The RayJob looks like the following:

{{% include "jobs/ray-job-sample.yaml" "yaml" %}}

You can run this RayJob with the following commands:

```sh
# Create the code ConfigMap (once)
kubectl apply -f ray-job-code-sample.yaml
# Create a RayJob. You can run this command multiple times
# to observe the queueing and admission of the jobs.
kubectl create -f ray-job-sample.yaml
```
