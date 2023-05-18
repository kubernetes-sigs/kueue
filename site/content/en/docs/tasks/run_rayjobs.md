---
title: "Run A RayJob"
date: 2023-05-18
weight: 6
description: >
  Run a Kueue scheduled RayJob.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [KubeRay's](https://ray-project.github.io/kuberay/) [RayJob](https://ray-project.github.io/kuberay/guidance/rayjob/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

By default the integration for `ray.io/rayjob` is not enabled, check the [Installation](/docs/installation/) for details on [Kueue's manger configuration](/docs/installation/#install-a-custom-configured-released-version).

Check [administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial cluster setup.

Check [KubeRay](https://ray-project.github.io/kuberay/) documentation for installation and configuration details.

## RayJob definition

Besides the usual RayJob configuration while using Kueue take into consideration the following aspects:

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

- A Kueue managed RayJob cannot use an existing cluster.
- The RayCluster should be deleted at the end of the job execution, `spec.ShutdownAfterJobFinishes` should be `true`.
- Because Kueue will reserve resources for the RayCluster, `spec.rayClusterSpec.enableInTreeAutoscaling` should be `false`.
- Because a Kueue workload can have a maximum of 8 PodSets, the maximum number of `spec.rayClusterSpec.workerGroupSpecs` is 7.

## Sample RayJob

```yaml
apiVersion: ray.io/v1alpha1
kind: RayJob
metadata:
  generateName: sample-rayjob-
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  shutdownAfterJobFinishes: true
  entrypoint: python /home/ray/samples/sample_code.py
  runtimeEnv: ewogICAgInBpcCI6IFsKICAgICAgICAicmVxdWVzdHM9PTIuMjYuMCIsCiAgICAgICAgInBlbmR1bHVtPT0yLjEuMiIKICAgIF0sCiAgICAiZW52X3ZhcnMiOiB7ImNvdW50ZXJfbmFtZSI6ICJ0ZXN0X2NvdW50ZXIifQp9Cg==
  rayClusterSpec:
    rayVersion: '2.4.0' # should match the Ray version in the image of the containers
    # Ray head pod template
    headGroupSpec:
      # the following params are used to complete the ray start: ray start --head --block --redis-port=6379 ...
      rayStartParams:
        dashboard-host: '0.0.0.0'
        num-cpus: '1' # can be auto-completed from the limits
      #pod template
      template:
        spec:
          containers:
            - name: ray-head
              image: rayproject/ray:2.4.0
              ports:
                - containerPort: 6379
                  name: gcs-server
                - containerPort: 8265 # Ray dashboard
                  name: dashboard
                - containerPort: 10001
                  name: client
                - containerPort: 8000
                  name: serve
              resources:
                limits:
                  cpu: "2"
                requests:
                  cpu: "1"
              volumeMounts:
                - mountPath: /home/ray/samples
                  name: code-sample
          volumes:
            # You set volumes at the Pod level, then mount them into containers inside that Pod
            - name: code-sample
              configMap:
                # Provide the name of the ConfigMap you want to mount.
                name: ray-job-code-sample
                # An array of keys from the ConfigMap to create as files
                items:
                  - key: sample_code.py
                    path: sample_code.py
    workerGroupSpecs:
      # the pod replicas in this group typed worker
      - replicas: 3
        minReplicas: 1
        maxReplicas: 5
        # logical group name, for this called small-group, also can be functional
        groupName: small-group
        rayStartParams: {}
        #pod template
        template:
          spec:
            containers:
              - name: ray-worker # must consist of lower case alphanumeric characters or '-', and must start and end with an alphanumeric character (e.g. 'my-name',  or '123-abc'
                image: rayproject/ray:2.4.0
                lifecycle:
                  preStop:
                    exec:
                      command: [ "/bin/sh","-c","ray stop" ]
                resources:
                  limits:
                    cpu: "2"
                  requests:
                    cpu: "1"
```

The provided example is based on KubeRay's [RayJob sample](https://github.com/ray-project/kuberay/blob/master/ray-operator/config/samples/ray_v1alpha1_rayjob.yaml), the target queue `kueue.x-k8s.io/queue-name: user-queue` being specified in the labels and `shutdownAfterJobFinishes` set to`true`.
