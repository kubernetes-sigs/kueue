---
title: "设置 MultiKueue 环境"
date: 2024-02-26
weight: 9
description: >
  设置 MultiKueue 集群所需的额外步骤。
---

本教程解释了如何在 MultiKueue 环境中配置管理集群和一个工作集群来运行 [JobSets](/zh-CN/docs/tasks/run_jobsets/#jobset-definition) 和 [batch/Jobs](/zh-CN/docs/tasks/run_jobs/#1-define-the-job)。

请查看概念部分了解 [MultiKueue 概述](/zh-CN/docs/concepts/multikueue/)。

假设您的管理集群名为 `manager-cluster`，工作集群名为 `worker1-cluster`。
要遵循本教程，请确保所有这些集群的凭据都存在于您本地机器的 kubeconfig 中。
查看 [kubectl 文档](https://kubernetes.io/zh-cn/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) 了解更多关于如何配置多集群访问的信息。

## 在工作集群中

{{% alert title="注意" color="primary" %}}
确保您当前的 _kubectl_ 配置指向工作集群。

运行：
```bash
kubectl config use-context worker1-cluster
```
{{% /alert %}}

当 MultiKueue 将工作负载从管理集群分发到工作集群时，它期望作业的命名空间和 LocalQueue 也存在于工作集群中。
换句话说，您应该确保工作集群配置在命名空间和 LocalQueues 方面与管理集群的配置保持一致。

要在 `default` 命名空间中创建示例队列设置，您可以应用以下清单：

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

### MultiKueue 专用 kubeconfig

为了在工作集群中委托 Job，管理集群需要能够创建、删除和监视工作负载及其父 Job。

当 `kubectl` 设置为使用工作集群时，下载：
{{< include "examples/multikueue/create-multikueue-kubeconfig.sh" "bash" >}}

然后运行：

```bash
chmod +x create-multikueue-kubeconfig.sh
./create-multikueue-kubeconfig.sh worker1.kubeconfig
```

这将创建一个 kubeconfig，可以在管理集群中使用它来委托当前工作集群中的 Job。

### Kubeflow 安装

在工作集群中安装 Kubeflow Trainer（有关更多详细信息，请参阅 [Kubeflow Trainer 安装](https://www.kubeflow.org/docs/components/training/installation/)）。请使用 v1.7.0 或更高版本以支持 MultiKueue。

## 在管理集群中

{{% alert title="注意" color="primary" %}}
确保您当前的 _kubectl_ 配置指向管理集群。

运行：
```bash
kubectl config use-context manager-cluster
```
{{% /alert %}}

### CRD 安装

有关与 MultiKueue 兼容的 CRD 安装，请参阅专用页面[这里](/zh-CN/docs/tasks/run/multikueue/)。

### 创建工作集群的 Kubeconfig 密钥

对于下一个示例，将 `worker1` 集群的 Kubeconfig 存储在名为 `worker1.kubeconfig` 的文件中，您可以通过运行以下命令创建 `worker1-secret` 密钥：

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```

有关 kubeconfig 生成的详细信息，请查看[工作集群](#multikueue-specific-kubeconfig)部分。

### 创建示例设置

应用以下配置来创建一个示例设置，其中提交到 ClusterQueue `cluster-queue` 的 Job 被委托给工作集群 `worker1`

{{< include "examples/multikueue/multikueue-setup.yaml" "yaml" >}}

配置成功后，创建的 ClusterQueue、AdmissionCheck 和 MultiKueueCluster 将变为活跃状态。

运行：
```bash
kubectl get clusterqueues cluster-queue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}CQ - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get admissionchecks sample-multikueue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}AC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker1 -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
```

期望输出如下：
```bash
CQ - Active: True Reason: Ready Message: Can admit new workloads
AC - Active: True Reason: Active Message: The admission check is active
MC - Active: True Reason: Active Message: Connected
```

## （可选）使用 Open Cluster Management 设置 MultiKueue

[Open Cluster Management (OCM)](https://open-cluster-management.io/) 是一个专注于 Kubernetes 应用程序多集群和多云场景的社区驱动项目。
它提供了一个强大、模块化和可扩展的框架，帮助其他开源项目跨多个集群编排、调度和管理工作负载。

与 OCM 的集成是一个可选的解决方案，它使 Kueue 用户能够简化 MultiKueue 设置过程，自动化生成 MultiKueue 专用的 Kubeconfig，并增强多集群调度能力。
有关此解决方案的更多详细信息，请参阅此[链接](https://github.com/open-cluster-management-io/ocm/tree/main/solutions/kueue-admission-check)。
