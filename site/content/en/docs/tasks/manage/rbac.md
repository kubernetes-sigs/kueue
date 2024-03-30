---
title: "Setup RBAC"
date: 2022-02-14
weight: 1
description: >
  Setup role-based access control (RBAC) in your cluster to control the types of users that can view and create Kueue objects.
---

This page shows you how to setup role-based access control (RBAC) in your cluster
to control the types of users that can view and create Kueue objects.

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).

This page assumes you are already familiar with [RBAC in kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/rbac/).

## ClusterRoles included in the installation

When you install Kueue, the following set of ClusterRoles are created for the
two main personas that we assume will interact with Kueue:

- `kueue-batch-admin-role` includes the permissions to manage ClusterQueues,
  Queues, Workloads, and ResourceFlavors.
- `kueue-batch-user-role` includes the permissions to manage [Jobs](https://kubernetes.io/docs/concepts/workloads/controllers/job/)
  and to view Queues and Workloads.

## Giving permissions to a batch administrator

A batch administrator typically requires the `kueue-batch-admin-role` ClusterRole
for all the namespaces.

To bind the `kueue-batch-admin-role` role to a batch administrator, represented
by the user `admin@example.com`, create a ClusterRoleBinding with a manifest
similar to the following:

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

To create the ClusterRoleBinding, save the preceding manifest and run the
following command:

```shell
kubectl apply -f batch-admin-role-binding.yaml
```

## Giving permissions to a batch user

A batch user typically requires permissions to:

- Create and view Jobs in their namespace.
- View the queues available in their namespace.
- View the status of their [Workloads](/docs/concepts/workload) in their namespace.

To give these permissions to a group of users `team-a@example.com` for the
namespace `team-a`, create a RoleBinding with a manifest similar to the
following:

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

To create the RoleBinding, save the preceding manifest and run the
following command:

```shell
kubectl apply -f team-a-batch-user-role-binding.yaml
```
