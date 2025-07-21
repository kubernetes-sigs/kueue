---
title: "运行 JobSet"
linkTitle: "Jobsets"
date: 2023-06-16
weight: 7
description: 在启用了kueue的环境里运行 Jobsets
---

本指南解释了如何使用 Kueue 的调度和服务管理功能运行 [JobSet Operator](https://github.com/kubernetes-sigs/jobset) [JobSet](https://jobset.sigs.k8s.io/docs/concepts/)。

本指南适用于 [批处理用户](/docs/tasks#batch-user)，他们需要对 Kueue 有基本的了解。更多信息，请参见 [Kueue 概览](/docs/overview)。

## 开始之前

1. 请参见 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

2. 请参见 [JobSet 安装](https://jobset.sigs.k8s.io/docs/installation/) 了解 JobSet Operator 的安装和配置详情。

{{% alert title="注意" color="primary" %}}
在 v0.8.1 之前，为了使用 JobSet，您需要重启 Kueue。您可以通过运行 `kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来完成此操作。
{{% /alert %}}

## JobSet 定义

当在 Kueue 上运行 [JobSets](https://jobset.sigs.k8s.io/docs/concepts/) 时，请考虑以下方面：

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 JobSet 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec.replicatedJobs` 中配置。还应考虑副本数量、[并行度](https://kubernetes.io/docs/concepts/workloads/controllers/job/#parallel-jobs) 和完成数量对资源计算的影响。

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

### c. 作业优先级

`spec.replicatedJobs` 中第一个非空的 [PriorityClassName](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#priorityclass) 将用作优先级。

```yaml
    - template:
        spec:
          template:
            spec:
              priorityClassName: high-priority
```

## 示例 JobSet

{{< include "examples/jobs/sample-jobset.yaml" "yaml" >}}

您可以使用以下命令运行此 JobSet：

```sh
# 为了监控队列和作业的准入，您可以多次运行此示例：
kubectl create -f sample-jobset.yaml
```

## Multikueue
请参见 [Multikueue](docs/tasks/run/multikueue) 了解在 MultiKueue 环境中运行 JobSets 的详细信息。
