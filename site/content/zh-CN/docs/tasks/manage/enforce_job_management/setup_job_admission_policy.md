---
title: "制定 Job 准入策略"
date: 2024-06-28
weight: 10
description: >
  实施验证准入策略以防止创建没有队列名称的 Job。
---

此页面展示了如何使用 Kubernetes
[验证准入策略](https://kubernetes.io/zh-cn/docs/reference/access-authn-authz/validating-admission-policy)，
基于[通用表达语言（CEL）](https://github.com/google/cel-spec)为 Kueue 设置 Job 准入策略。

## 开始之前

确保满足以下条件：

- 一个 Kubernetes 集群正在运行。
- 已启用 `ValidatingAdmissionPolicy` **特性门控**。在 Kubernetes 1.30 或更新版本中，默认启用该特性门控。
- kubectl 命令行工具能够与你的集群通信。
- [Kueue 已安装](/zh-CN/docs/installation)。

## 示例

下面的例子展示了如何设置 Job 准入策略，以拒绝所有未包含 queue-name
且发送到标记为 `kueue-managed` 命名空间的 Job 或 JobSets。

你应该在 Kueue 配置中将 `manageJobsWithoutQueueName` 设置为 `false`，
以便让管理员能够在未标记为 `kueue-managed` 的命名空间中执行 Job。
发送到未标记命名空间的 Job 不会被拒绝，也不会被 Kueue 管理。

{{< include "examples/sample-validating-policy.yaml" "yaml" >}}

要创建此策略，请下载上述文件并运行以下命令：

```shell
kubectl create -f sample-validating-policy.yaml
```

然后，通过创建一个 `ValidatingAdmissionPolicyBinding` 将准入策略应用到命名空间。
策略绑定将命名空间链接到定义的准入策略，并指示 Kubernetes 如何响应验证结果。

以下是策略绑定（Binding）的一个示例：

{{< include "examples/sample-validating-policy-binding.yaml" "yaml" >}}

要创建绑定，请下载上述文件并运行以下命令：

```shell
kubectl create -f sample-validating-policy-binding.yaml
```

运行以下命令以标记你希望实施此策略的每个命名空间：

```shell
kubectl label namespace my-user-namespace 'kueue-managed=true'
```

现在，当你尝试在任何标记为 kueue-managed 的命名空间中创建没有
`kueue.x-k8s.io/queue-name` 标签或值的 `Job` 或 `JobSet` 时，
错误信息将类似于如下：

```
ValidatingAdmissionPolicy 'sample-validating-admission-policy' with binding 'sample-validating-admission-policy-binding' denied request: The label 'kueue.x-k8s.io/queue-name' is either missing or does not have a value set.
```

## 父/子关系的处理

这种方法的一个复杂之处在于，当 Workload 被接纳时，它可能会创建一些由
Kueue 管理的 Kind 的子资源。这些子资源不需要拥有队列名称标签，
因为它们的接纳是由其父资源的接纳隐式保证的。在示例策略中，`exclude-jobset-owned` `matchCondition`
确保了由父 `JobSet` 拥有的子 `Job` 不会被拒绝接纳。
