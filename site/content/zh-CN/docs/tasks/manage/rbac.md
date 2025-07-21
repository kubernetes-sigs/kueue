---
title: "配置 RBAC"
date: 2022-02-14
weight: 1
description: 在集群中配置基于角色的访问控制（RBAC），以控制哪些类型的用户可以查看和创建 Kueue 对象。
---

本页面向您展示如何在集群中配置基于角色的访问控制（RBAC），以控制哪些类型的用户可以查看和创建 Kueue 对象。

本页面面向[批处理管理员](/docs/tasks#batch-administrator)。

## 在你开始之前

请确保满足以下条件：

- Kubernetes 集群已运行。
- kubectl 命令行工具已能与您的集群通信。
- [已安装 Kueue](/docs/installation)。

本页面假设您已熟悉 [Kubernetes 中的 RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)。

## 安装时包含的 ClusterRole

当您安装 Kueue 时，会为我们假定的两类主要用户创建以下 ClusterRole：

- `kueue-batch-admin-role` 包含管理 ClusterQueue、Queue、Workload 和 ResourceFlavor 的权限。
- `kueue-batch-user-role` 包含管理 [Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/) 以及查看 Queue 和 Workload 的权限。

## 授权批处理管理员权限

批处理管理员通常需要在所有命名空间拥有 `kueue-batch-admin-role` ClusterRole。

要将 `kueue-batch-admin-role` 角色绑定到批处理管理员（如用户 `admin@example.com`），请创建如下的 ClusterRoleBinding 清单：

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

要创建 ClusterRoleBinding，请保存上述清单并运行以下命令：

```shell
kubectl apply -f batch-admin-role-binding.yaml
```

## 授权批处理用户权限

批处理用户通常需要以下权限：

- 在其命名空间中创建和查看 Job。
- 查看其命名空间中可用的队列。
- 查看其命名空间中 [Workload](/docs/concepts/workload) 的状态。

要为用户组 `team-a@example.com` 在命名空间 `team-a` 授予这些权限，请创建如下的 RoleBinding 清单：

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
