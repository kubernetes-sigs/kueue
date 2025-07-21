---
title: "配置 MultiKueue 环境"
date: 2024-02-26
weight: 9
description: 配置 multikueue 集群所需的额外步骤。
---

本教程介绍如何配置一个管理集群和一个工作集群，在 MultiKueue 环境中运行 [JobSets](/docs/tasks/run_jobsets/#jobset-definition) 和 [batch/Jobs](/docs/tasks/run_jobs/#1-define-the-job)。

请查阅概念部分的 [MultiKueue 概述](/docs/concepts/multikueue/)。

假设您的管理集群名为 `manager-cluster`，工作集群名为 `worker1-cluster`。
要跟随本教程，请确保所有这些集群的凭据已存在于本地机器的 kubeconfig 中。
查阅 [kubectl 文档](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) 了解如何配置多集群访问。

## 在工作集群中

{{% alert title="注意" color="primary" %}}
请确保当前 _kubectl_ 配置指向工作集群。

运行：
```bash
kubectl config use-context worker1-cluster
```
{{% /alert %}}

当 MultiKueue 从管理集群调度工作负载到工作集群时，期望作业的命名空间和 LocalQueue 在工作集群中也存在。
换句话说，您应确保工作集群的配置在命名空间和 LocalQueue 上与管理集群保持一致。

要在 `default` 命名空间中创建示例队列设置，可以应用以下清单：

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

### MultiKueue 专用 Kubeconfig

为了在工作集群中委派作业，管理集群需要能够创建、删除和监视工作负载及其父作业。

在 `kubectl` 已设置为使用工作集群时，下载：
{{< include "examples/multikueue/create-multikueue-kubeconfig.sh" "bash" >}}

并运行：

```bash
chmod +x create-multikueue-kubeconfig.sh
./create-multikueue-kubeconfig.sh worker1.kubeconfig
```

以创建可在管理集群中用于委派当前工作集群作业的 Kubeconfig。

### Kubeflow 安装

在工作集群中安装 Kubeflow Trainer（详见 [Kubeflow Trainer 安装](https://www.kubeflow.org/docs/components/training/installation/)）。MultiKueue 请使用 v1.7.0 或更高版本。

## 在管理集群中

{{% alert title="注意" color="primary" %}}
请确保当前 _kubectl_ 配置指向管理集群。

运行：
```bash
kubectl config use-context manager-cluster
```
{{% /alert %}}

### CRD 安装

关于与 MultiKueue 兼容的 CRD 安装，请参考[专用页面](/docs/tasks/run/multikueue/)。

### 创建工作集群的 Kubeconfig Secret

在下例中，假设 `worker1` 集群的 Kubeconfig 保存在名为 `worker1.kubeconfig` 的文件中，可以通过以下命令创建 `worker1-secret` Secret：

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```

有关 Kubeconfig 生成的详细信息，请查阅[工作集群](#multikueue-specific-kubeconfig)部分。

### 创建示例配置

应用以下内容，在 ClusterQueue `cluster-queue` 中提交的作业被委派到 worker `worker1` 的示例配置：

{{< include "examples/multikueue/multikueue-setup.yaml" "yaml" >}}

配置成功后，创建的 ClusterQueue、AdmissionCheck 和 MultiKueueCluster 将变为激活状态。

运行：
```bash
kubectl get clusterqueues cluster-queue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}CQ - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get admissionchecks sample-multikueue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}AC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker1 -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
```

预期输出如下：
```bash
CQ - Active: True Reason: Ready Message: Can admit new workloads
AC - Active: True Reason: Active Message: The admission check is active
MC - Active: True Reason: Active Message: Connected
```

## （可选）结合 Open Cluster Management 配置 MultiKueue

[Open Cluster Management (OCM)](https://open-cluster-management.io/) 是一个社区驱动的项目，专注于 Kubernetes 应用的多集群和多云场景。
它提供了一个强大、模块化且可扩展的框架，帮助其他开源项目在多个集群间编排、调度和管理工作负载。

与 OCM 的集成是可选方案，可帮助 Kueue 用户简化 MultiKueue 配置流程、自动生成 MultiKueue 专用 Kubeconfig，并增强多集群调度能力。
更多详情请参考此[链接](https://github.com/open-cluster-management-io/ocm/tree/main/solutions/kueue-admission-check)。
