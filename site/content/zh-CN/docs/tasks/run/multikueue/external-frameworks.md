---
title: "在多集群中运行外部框架 Job"
linkTitle: "外部框架 Job"
weight: 5
date: 2025-09-19
description: >
  运行 Multi Kueue 调度器外部框架 Job。
---

{{< feature-state state="alpha" for_version="v0.14" >}}

## 开始之前

1. 检查[多 Kueue 安装指南](/docs/tasks/manage/setup_multikueue)以了解如何正确设置多 Kueue 集群。

2. 启用 `MultiKueueAdaptersForCustomJobs` 特性门控。此特性处于 Alpha 阶段，默认情况下是禁用的。

   要启用此特性，请将 `--feature-gates=MultiKueueAdaptersForCustomJobs=true`
   标志添加到 Kueue 控制器管理器的参数中。例如，你可以使用以下命令将其应用到你的部署中：

   ```bash
   kubectl patch deployment kueue-controller-manager -n kueue-system \
     --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--feature-gates=MultiKueueAdaptersForCustomJobs=true"}]'
   ```

3. 确保 Kueue 控制器对你的外部 GVK 有权限。以 Tekton PipelineRuns 为例：

   ```yaml
   apiVersion: rbac.authorization.k8s.io/v1
   kind: ClusterRole
   metadata:
     name: kueue-external-frameworks
   rules:
   - apiGroups: ["tekton.dev"]
     resources: ["pipelineruns", "pipelineruns/status"]
     verbs: ["get", "list", "watch", "patch", "update"]
   ```

## MultiKueue 集成

MultiKueue 外部框架支持允许你配置 MultiKueue 以适用于遵循
Job 模式的任何自定义资源（CR）。这对于集成如下的 CRD 特别有用：

- Tekton `PipelineRun`
- Argo `Workflow`
- 其他自定义 Job 类型

要由通用的 MultiKueue 适配器管理，自定义资源必须包含 `.spec.managedBy` 字段。
当一个 CR 被设计为由 MultiKueue 管理时，此字段必须设置为 `"kueue.x-k8s.io/multikueue"`。
适配器使用这个字段来识别要管理的对象。

外部框架在 Kueue `Configuration` 对象中配置。
设置位于 `multikueue.externalFrameworks` 下。此字段保存要启用的框架列表。

`externalFrameworks` 列表中的每个条目都是一个对象，具有以下字段：

| 字段      | 类型   | 必需 | 描述                                       |
|------------|--------|----------|---------------------------------------------------|
| `name`     | string | 是      | 资源的 GVK 格式为 `Kind.version.group`。 |

## 示例：Tekton PipelineRun

为了演示如何配置适配器，我们以 Tekton `PipelineRun` 为例。

首先，更新你的 Kueue 配置，将 `PipelineRun` 添加到 `externalFrameworks` 列表中：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
data:
  multikueue:
    externalFrameworks:
      - name: "PipelineRun.v1.tekton.dev"
```

一旦配置完成，通用的 MultiKueue 适配器将监视 `.spec.managedBy`
设置为 `"kueue.x-k8s.io/multikueue"` 的 `PipelineRun` 资源，
并像管理其他支持的 Job 类型一样管理它们。
这使得 Kueue 可以跨多个集群处理 `PipelineRun` 对象的资源管理。

{{% alert title="注意" color="primary" %}}
注意：对于外部框架 Job，在管理集群上 Kueue 默认将 `spec.managedBy`
字段设置为 `kueue.x-k8s.io/multikueue`。

这允许外部框架控制器忽略管理集群上由 MultiKueue 管理的 Job，特别是跳过包裹资源的创建。

资源将在选定的工作集群上的外部框架 Job 镜像副本中创建，实际计算也会在那里发生。
外部框架 Job 的镜像副本不设置该字段。
{{% /alert %}}
