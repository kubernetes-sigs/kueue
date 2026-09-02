---
title: "设置 MultiKueue 环境"
date: 2024-02-26
weight: 9
description: >
  设置 MultiKueue 集群所需的额外步骤。
---

本教程解释了如何在 MultiKueue 环境中配置管理集群和一个工作集群来运行 [JobSets](/zh-cn/docs/tasks/run/jobsets/#jobset-definition) 和 [batch/Jobs](/zh-cn/docs/tasks/run/jobs/#1-define-the-job)。

请查看概念部分了解 [MultiKueue 概述](/zh-cn/docs/concepts/multikueue/)。

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

还必须用同名管理集群 Namespace 的 UID 显式授权工作集群 Namespace。
对于本教程中的 `default` Namespace，请运行：

```bash
manager_namespace_uid=$(kubectl --context manager-cluster get namespace default -o jsonpath='{.metadata.uid}')
kubectl --context worker1-cluster annotate namespace default \
  kueue.x-k8s.io/multikueue-allowed-manager-namespace-uids="[\"${manager_namespace_uid}\"]" \
  --overwrite
```

每一对可能接收 MultiKueue 工作负载的管理/工作 Namespace 都需要此注解。
如果管理 Namespace 被删除并重新创建，其 UID 会改变，因此必须重新授权。

{{% alert title="安全提示" color="warning" %}}

同名 Namespace 是安全边界，而不只是命名约定。MultiKueue 会复制工作负载规范，
其中的 ServiceAccount、Secret、ConfigMap、镜像拉取 Secret 和 PVC 等引用会在工作集群
Namespace 中解析。仅应配对两端用户具有等价权限的 Namespace；多租户环境应使用专用的
工作 Namespace 和准入策略。移除此注解会停止远程状态同步和新对象创建，但 MultiKueue
仍可清理以前分发的对象。

{{% /alert %}}

要在 `default` 命名空间中创建示例队列设置，您可以应用以下清单：

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

### MultiKueue 专用 kubeconfig

为了在工作集群中委托 Job，管理集群需要能够创建、删除和监视工作负载及其父 Job。

示例脚本创建的是集群范围的权限，并额外授予读取 Namespace 的权限以校验 UID 绑定。
它不会按 Namespace 提供租户隔离，也不应获得修改 Namespace 的权限。

当 `kubectl` 设置为使用工作集群时，下载：
{{< include "examples/multikueue/create-multikueue-kubeconfig.sh" "bash" >}}

然后运行：

```bash
chmod +x create-multikueue-kubeconfig.sh
./create-multikueue-kubeconfig.sh worker1.kubeconfig
```

这将创建一个 kubeconfig，可以在管理集群中使用它来委托当前工作集群中的 Job。

升级现有安装时，应先为每个工作集群凭据添加 core `namespaces` 的 `get` 权限并完成上述
Namespace 注解，然后再滚动升级所有管理器副本。旧副本不会执行此校验，因此混合版本
部署在升级完成前不具备新的安全保证。如果无法预先迁移，可以暂时启用
`MultiKueueAllowUnboundWorkerNamespaces=true`，完成 RBAC 和注解迁移后，再在所有管理器
副本上禁用此特性门控。

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

有关与 MultiKueue 兼容的 CRD 安装，请参阅专用页面[这里](/zh-cn/docs/tasks/run/multikueue/)。

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
