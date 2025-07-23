---
title: "设置 RBAC"
date: 2022-02-14
weight: 1
description: >
  在集群中设置基于角色的访问控制（RBAC），以控制可以查看和创建 Kueue 对象的用户类型。
---

This page shows you how to setup role-based access control (RBAC) in your cluster
to control the types of users that can view and create Kueue objects.

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

本页面介绍了如何在集群中设置基于角色的访问控制（RBAC），以控制可以查看和创建 Kueue 对象的用户类型。

本页面适用于[批处理管理员](/zh-cn/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具与你的集群通信。
- [Kueue 已安装](/zh-CN/docs/installation)。

本页面假设你已经熟悉 [Kubernetes 中的 RBAC](https://kubernetes.io/zh-cn/docs/reference/access-authn-authz/rbac/)。

## 安装步骤中包含的 ClusterRole  {#clusterroles-included-in-the-installation}

当你安装 Kueue 时，会为预计将与 Kueue 交互的两个主要角色创建以下 ClusterRole：

- `kueue-batch-admin-role` 包含管理 ClusterQueues、Queues、Workloads 和 ResourceFlavors 的权限。
- `kueue-batch-user-role` 包含管理 [Job](https://kubernetes.io/zh-cn/docs/concepts/workloads/controllers/job/)
  并查看 Queues 和 Workloads 的权限。

## 向批处理管理员授予权限  {#giving-permissions-to-a-batch-administrator}

批处理管理员通常需要 `kueue-batch-admin-role` ClusterRole 对所有命名空间进行操作。

要将 `kueue-batch-admin-role` 角色绑定到由用户 `admin@example.com`
表示的批处理管理员，需创建一个类似的 ClusterRoleBinding 清单：

```yaml
# batch-admin-role-binding.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: read-pods
subjects:
- kind: User
  name: admin@example.com
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: kueue-batch-admin-role
  apiGroup: rbac.authorization.k8s.io
```

要创建 ClusterRoleBinding，保存上述清单并运行以下命令：

```shell
kubectl apply -f batch-admin-role-binding.yaml
```

## 给批处理用户授予权限  {#giving-permissions-to-a-batch-user}

批处理用户通常需要以下权限：

- 在其命名空间中创建和查看 Job。
- 查看其命名空间中可用的队列。
- 查看其命名空间中**工作负载**的状态。

要为 `team-a@example.com` 用户组在命名空间 `team-a` 中授予这些权限，
可以创建一个 RoleBinding，并使用类似于下面的清单：

```yaml
# team-a-batch-user-role-binding.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: read-pods
  namespace: team-a
subjects:
- kind: Group
  name: team-a@example.com
  apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: kueue-batch-user-role
  apiGroup: rbac.authorization.k8s.io
```

要创建 RoleBinding，请保存上述清单并运行以下命令：

```shell
kubectl apply -f team-a-batch-user-role-binding.yaml
```
