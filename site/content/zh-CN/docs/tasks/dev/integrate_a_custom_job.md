---
title: "将自定义 Job 与 Kueue 集成"
date: 2023-07-25
weight: 8
description: >
  将自定义 Job 与 Kueue 集成。
---

Kueue 为多种 Job 类型提供了内置集成，包括
Kubernetes batch Job、MPIJob、RayJob 和 JobSet。

使用 Kueue 管理缺乏内置集成的类 Job CRD 有三种选择：

- 通过将自定义 Job 实例包装在 AppWrapper 中来利用内置的 AppWrapper 集成。
  详情请参阅[运行包装的自定义工作负载](/docs/tasks/run/wrapped_custom_workload)。
- 作为 Kueue 仓库的一部分构建新的集成。
- 作为外部控制器构建新的集成。

本指南面向[平台开发者](/docs/tasks#platform-developer)，描述如何构建新的集成。
集成应该使用 Kueue 的 `jobframework` 包提供的 API 来构建。
这既会简化开发，也会确保如果你的 Job 类型被社区广泛使用，
你的控制器将被正确构造以成为核心内置集成。

## 需求概览 {#overview-of-requirements}

Kueue 使用 [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)。
我们建议在开始构建 Kueue 集成之前，先熟悉它和
[Kubebuilder](https://github.com/kubernetes-sigs/kubebuilder)。

无论你是构建外部集成还是内置集成，你的主要任务如下：

1. 要与 Kueue 配合工作，你的自定义 Job CRD 应该有一个类似暂停的字段，
   其语义类似于 [Kubernetes Job 中的 `suspend` 字段](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job)。
   该字段需要在你的 CRD 的 `spec` 中，而不是在其 `status` 中，
   以便可以从 webhook 设置其值。你的 CRD 的主控制器必须响应此字段值的更改，
   通过暂停或恢复其拥有的资源。

2. 你需要将你的 CRD 的 GroupVersionKind 注册为 Kueue 的集成。

3. 你需要为你的 CRD 实例化 Kueue 的 `jobframework` 包的各个部分：
   - 你需要为你的 CRD 实现 Kueue 的 `GenericJob` 接口
   - 你需要实例化一个 `ReconcilerFactory` 并将其注册到 controller runtime。
   - 你需要为你的 CRD 添加 `+kubebuilder:rbac` 指令，以便 Kueue 被允许管理它。
   - 你需要注册 webhook，在你的 CRD 实例中设置 `suspend` 字段的初始值，
     并在创建和更新操作时验证 Kueue 不变量。
   - 你需要为你的 CRD 实例化一个 `Workload` 索引器。

## 构建内置集成 {#building-a-built-in-integration}

首先，在 `./pkg/controller/jobs/` 中添加一个新文件夹来承载集成的实现。

以下是你可以学习的已完成的内置集成：

- [BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
- [JobSet](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/jobset)
- [MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob)
- [RayJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/rayjob)

### 注册 {#registration}

在 [controller_manager_config.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/config/components/manager/controller_manager_config.yaml)
的 `.integrations.frameworks` 中添加你的框架名称。

使用 [kubebuilder 标记注释](https://book.kubebuilder.io/reference/markers/rbac.html)
为你的 CRD 添加 RBAC 授权。

编写一个调用 jobframework `RegisterIntegration()` 函数的 go `func init()`。

### Job Framework {#job-framework}

在你的文件夹中的 `mycrd_controller.go` 文件中，实现 `GenericJob` 接口，
以及[框架定义的其他可选接口](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go)。

在你的文件夹中的 `mycrd_webhook.go` 文件中，提供调用 jobframework 中的辅助方法的 webhook，
以设置创建的作业的初始暂停状态并验证不变量。

为控制器和 webhook 添加测试文件。你可以查看 `./pkg/controller/jobs/`
其他子文件夹中的测试文件来学习如何实现它们。

### 开发变更和验证 webhook {#developing-a-mutation-and-validation-webhook}

一旦你实现了 `GenericJob` 接口，你可以通过使用 `pkg/controller/jobframework`
中的 `BaseWebhookFactory` 为该类型注册 webhook。此 webhook 确保作业在暂停状态下创建，
并且队列名称在作业被准入时不会更改。你可以阅读
[MPIJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/mpijob)
集成作为示例。

对于某些用例，你可能需要注册专用的 webhook。在这种情况下，你应该使用
Kueue 的 `LosslessDefaulter`，它确保默认器与 CRD 的未来版本兼容，
前提是默认器**不会**从原始对象中删除任何字段。你可以阅读
[BatchJob](https://github.com/kubernetes-sigs/kueue/tree/main/pkg/controller/jobs/job)
集成作为如何使用 `LosslessDefaulter` 的示例。

如果上述建议与你的用例不兼容，请注意
[使用 controller-runtime 的 CustomDefaulter 时的潜在问题](https://groups.google.com/a/kubernetes.io/g/wg-batch/c/WAaabCuGCoY)。

### 调整构建系统 {#adjust-build-system}

添加编译代码所需的依赖项。例如，使用 `go get github.com/kubeflow/mpi-operator@0.4.0`。

更新 [Makefile](https://github.com/kubernetes-sigs/kueue/blob/main/Makefile) 进行测试。

- 添加将你的自定义作业的 CRD 复制到 Kueue 项目的命令。
- 将你的自定义作业操作器 CRD 依赖项添加到 `test-integration` 中。

## 构建外部集成 {#building-an-external-integration}

构建外部集成类似于构建内置集成，
不同之处在于你将在单独的可执行文件中为你的 CRD 实例化 Kueue 的 `jobframework`。
你对主要 Kueue 管理器所做的唯一更改是部署时配置更改，
以使其能够识别你的 CRD 正在由与 Kueue 兼容的管理器管理。

以下是你可以学习的已完成的外部集成：

- [AppWrapper](https://github.com/project-codeflare/appwrapper)

### 注册 {#registration}

通过将你的框架的 GroupVersionKind 添加到
[controller_manager_config.yaml](https://kueue.sigs.k8s.io/docs/installation/#install-a-custom-configured-released-version)
的 `.integrations.externalFrameworks` 中来配置 Kueue。

### Job Framework {#job-framework}

对你的 CRD 的现有基于 controller-runtime 的管理器进行以下更改。

在你的 `go.mod` 中添加对 Kueue 的依赖项，以便你可以将 `jobframework` 作为 go 库导入。

在新的 `mycrd_workload_controller.go` 文件中，实现 `GenericJob` 接口，
以及[框架定义的其他可选接口](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/interface.go)。

扩展你的 CRD 的现有 webhook 以调用 Kueue 的 webhook 辅助方法：

- 你的默认器应该调用 [defaults.go](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/defaults.go)
  中的 `jobframework.ApplyDefaultForSuspend`
- 你的验证器应该调用 [validation.go](https://github.com/kubernetes-sigs/kueue/blob/main/pkg/controller/jobframework/validation.go)
  中的 `jobframework.ValidateJobOnCreate` 和 `jobframework.ValidateJobOnUpdate`

扩展你的管理器的启动程序以执行以下操作：

- 使用 `jobframework.NewGenericReconcilerFactory` 方法，
  为你的 CRD 创建 Kueue 的 JobReconciler 实例并将其注册到 controller-runtime 管理器。
- 调用 `jobframework.SetupWorkloadOwnerIndex` 为你的 CRD 拥有的 Workload 创建索引器。

扩展你现有的 RBAC 授权以包括 Kueue 的 JobReconciler 所需的权限：

```go
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
```

有关具体示例，请参考 AppWrapper 控制器的以下部分：

- [workload_controller.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/controller/workload/workload_controller.go)
- [appwrapper_webhook.go](https://github.com/project-codeflare/appwrapper/blob/main/internal/webhook/appwrapper_webhook.go)
- [setup.go](https://github.com/project-codeflare/appwrapper/blob/main/pkg/controller/setup.go)
