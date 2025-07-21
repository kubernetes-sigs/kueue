---
title: "运行 RayJob"
linkTitle: "RayJobs"
date: 2024-08-07
weight: 6
description: 在启用了kueue的环境里运行 RayJob
---

本页面展示了如何利用 Kueue 的调度和服务管理能力来运行 [KubeRay](https://github.com/ray-project/kuberay)
[RayJob](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayjob-quick-start.html)。

本指南适用于 [批处理用户](/docs/tasks#batch-user) ，他们基本了解 Kueue。更多信息，请参见 [Kueue 概览](/docs/overview)。

## 开始之前

1. 请确保您使用 Kueue v0.6.0 版本或更高版本，以及 KubeRay v1.1.0 或更高版本。

2. 请参见 [管理集群配额](/docs/tasks/manage/administer_cluster_quotas) 了解初始 Kueue 设置的详细信息。

3. 请参见 [KubeRay 安装](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/raycluster-quick-start.html#step-2-deploy-a-kuberay-operator) 了解 KubeRay 的安装和配置详情。

{{% alert title="注意" color="primary" %}}
在 v0.8.1 之前，您需要重启 Kueue 才能使用 RayJob。您可以通过运行 `kubectl delete pods -l control-plane=controller-manager -n kueue-system` 来完成此操作。
{{% /alert %}}

## RayJob 定义

当运行 [RayJobs](https://docs.ray.io/en/latest/cluster/kubernetes/getting-started/rayjob-quick-start.html) 时，请考虑以下方面：

### a. 队列选择

目标 [本地队列](/docs/concepts/local_queue) 应在 RayJob 配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec.rayClusterSpec` 中配置。

```yaml
spec:
  rayClusterSpec:
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

### c. 限制

- 一个 Kueue 管理的 RayJob 不能使用现有的 RayCluster。
- RayCluster 应在作业执行结束后删除，`spec.ShutdownAfterJobFinishes` 应为 `true`。
- 因为 Kueue 会为 RayCluster 预留资源，`spec.rayClusterSpec.enableInTreeAutoscaling` 应为 `false`。
- 因为一个 Kueue 工作负载最多可以有 8 个 PodSets，`spec.rayClusterSpec.workerGroupSpecs` 的最大数量为 7。

## 示例 RayJob

在本例中，代码通过 ConfigMap 提供给 Ray 框架。

{{< include "examples/jobs/ray-job-code-sample.yaml" "yaml" >}}

RayJob 如下所示：

{{< include "examples/jobs/ray-job-sample.yaml" "yaml" >}}

您可以使用以下命令运行此 RayJob：

```sh
# 创建代码 ConfigMap（一次）
kubectl apply -f ray-job-code-sample.yaml
# 创建 RayJob。您可以多次运行此命令，以观察作业的排队和准入。
kubectl create -f ray-job-sample.yaml
```

{{% alert title="注意" color="primary" %}}
上述示例来自 [这里](https://raw.githubusercontent.com/ray-project/kuberay/v1.1.1/ray-operator/config/samples/ray-job.sample.yaml) 
并且只添加了 `queue-name` 标签和更新了请求。
{{% /alert %}}