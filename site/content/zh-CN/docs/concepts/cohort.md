---
title: "Cohort"
date: 2025-04-16
weight: 4
description: >
  集群范围资源，用于组织配额
---

## 你好，Cohort

Cohort 使你能够组织你的配额。同一个 Cohort（或对于[分层 Cohort](#hierarchical-cohorts)
来说是同一个 CohortTree）中的 ClusterQueue 可以相互共享资源。
最简单的 Cohort 如下：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Cohort
metadata:
  name: "hello-cohort"
```

ClusterQueue 可以通过引用来加入此 Cohort：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "my-cluster-queue"
spec:
  cohort: "hello-cohort"
```

## 配置配额

资源配额可以在 Cohort 级别定义（类似于为
[ClusterQueue](/zh-CN/docs/concepts/cluster_queue/#flavors-and-resources)
定义的方式），并由 Cohort 内的 ClusterQueue 来消耗使用。请注意，
在 Cohort 级别定义的 `nominalQuota` 表示除了 Cohort 内 ClusterQueue
所定义的资源之外的**额外资源**。Cohort 的 `nominalQuota` 可以被视为其内
ClusterQueue 的共享池。此外，此配额也可能根据 LendingLimit 借给父 Cohort。

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Cohort
metadata:
  name: "hello-cohort"
spec:
  resourceGroups:
    - coveredResources: ["cpu"]
      flavors:
      - name: "default-flavor"
        resources:
        - name: "cpu"
          # 此队列中的 ClusterQueue 可使用的共享配额
          nominalQuota: 12
```

为了让 ClusterQueue 从其 Cohort 借用资源，它**必须**为所需的资源和
Flavor 定义 nominal 配额 - 即使该值为 0。

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "my-cluster-queue"
spec:
  cohort: "hello-cohort"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        # ClusterQueue 没有任何 nominal 配额，
        # 但是可以使用 Cohort 中定义的资源
        #（或由其他 ClusterQueue 共享的资源）。在这种情况下，
        # ClusterQueue 可以访问其 Cohort 定义的 12 个共享 CPU。
        nominalQuota: 0
```

## 分层 Cohort  {#ierarchical-cohorts}

Cohort 可以组织成树形结构。我们将属于同一棵树的 ClusterQueue 和
Cohort 的组合称为 **CohortTree**。

给定 CohortTree 中的 ClusterQueue 可以使用其中的资源，
并遵循[借用和借出限制](/zh-CN/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ResourceQuota)。
这些借用和借出限制可以为 Cohort 以及 ClusterQueue 指定。

这是一个简单的 CohortTree，包含三个 Cohort：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Cohort
metadata:
  name: "root-cohort"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: Cohort
metadata:
  name: "important-org"
spec:
  parentName: "root-cohort"
  fairSharing:
    weight: "0.75"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: Cohort
metadata:
  name: "regular-org"
spec:
  parentName: "root-cohort"
  fairSharing:
    weight: "0.25"
```

此示例假设启用了公平共享。在这种情况下，重要的组织将趋向于使用
75% 的公共资源，而普通组织则趋向于使用 25%。
