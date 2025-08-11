---
title: "管理集群配额"
date: 2022-03-14
weight: 2
description: >
  管理你的集群资源配额，并在租户之间建立公平共享规则。
---

本页面向你展示如何管理集群资源配额，
并在租户之间建立公平共享规则。

本页面的目标读者是[批处理管理员](/zh-CN/docs/tasks#batch-administrator)。

## 你开始之前 {#before-you-begin}

请确保满足以下条件：

- Kubernetes 集群已运行。
- kubectl 命令行工具能与你的集群通信。
- [已安装 Kueue](/zh-CN/docs/installation)。

## 安装单一 ClusterQueue 和 ResourceFlavor {#single-clusterqueue-and-single-resourceflavor-setup}

在以下步骤中，你将创建一个排队系统用于管理集群的配额，它拥有
一个 ClusterQueue 和一个 [ResourceFlavor](/zh-CN/docs/concepts/cluster_queue#resourceflavor-object)。

你可以通过 apply [examples/admin/single-clusterqueue-setup.yaml](/examples/admin/single-clusterqueue-setup.yaml)一次性完成这些步骤：

```shell
kubectl apply -f examples/admin/single-clusterqueue-setup.yaml
```

### 1. 创建 [ClusterQueue](/zh-CN/docs/concepts/cluster_queue) {#1-create-clusterqueue}

创建一个 ClusterQueue 来表示整个集群的资源配额。

编写 ClusterQueue 的清单文件。应类似如下：

```yaml
# cluster-queue.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: 36Gi
```

要创建 ClusterQueue，请运行以下命令：

```shell
kubectl apply -f cluster-queue.yaml
```

此 ClusterQueue 管理[资源类型](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-types)
`cpu` 和 `memory` 的使用。每种资源类型都有一个名为 `default` 的 [ResourceFlavor](/zh-CN/docs/concepts/cluster_queue#resourceflavor-object)，
并设置了名义配额。

空的 `namespaceSelector` 允许任何命名空间使用这些资源。

### 2. 创建 [ResourceFlavor](/zh-CN/docs/concepts/cluster_queue#resourceflavor-object) {#2-create-resourceflavor}

ClusterQueue 目前还不能使用，因为 `default` 风味尚未定义。

通常，ResourceFlavor 有节点标签和/或污点，用于限定哪些节点可以提供此 Flavor。
但由于我们使用单一风味来代表
集群中所有可用资源，你可以创建一个空的 ResourceFlavor。

编写 ResourceFlavor 的清单文件。应类似如下：

```yaml
# default-flavor.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
```

要创建 ResourceFlavor，请运行以下命令：

```shell
kubectl apply -f default-flavor.yaml
```

`.metadata.name` 字段需与 ClusterQueue 中 `.spec.resourceGroups[0].flavors[0].name`
字段一致。

### 3. 创建 [LocalQueues](/zh-CN/docs/concepts/local_queue) {#3-create-localqueues}

用户不能直接将[工作负载](/zh-CN/docs/concepts/workload)
发送到 ClusterQueue。
用户需要将工作负载发送到其命名空间中的队列。
因此，为了使排队系统完整，
你需要在每个需要访问 ClusterQueue 的命名空间中创建一个队列。

编写 LocalQueue 的清单文件。应类似如下：

```yaml
# default-user-queue.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
```

要创建 LocalQueue，请运行以下命令：

```shell
kubectl apply -f default-user-queue.yaml
```

## 多 ResourceFlavor 设置 {#multiple-resourceflavors-setup}

你可以为不同的 [ResourceFlavor](/zh-CN/docs/concepts/cluster_queue#resourceflavor-object)定义配额。

本节其余部分假设你的集群有两种 CPU 架构的节点，
分别为 `x86` 和 `arm`，通过节点标签 `cpu-arch` 指定。

### 1. 创建 ResourceFlavors {#1-create-resourceflavors}

编写 ResourceFlavor 的清单文件。
应类似如下：

```yaml
# flavor-x86.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "x86"
spec:
  nodeLabels:
    cpu-arch: x86
```

```yaml
# flavor-arm.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "arm"
spec:
  nodeLabels:
    cpu-arch: arm
```

要创建 ResourceFlavor，请运行以下命令：

```shell
kubectl apply -f flavor-x86.yaml -f flavor-arm.yaml
```

ResourceFlavor 中设置的标签应与你的节点标签一致。
如果你使用 [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
或类似控制器，请确保其配置为在添加新节点时添加这些标签。

### 2. 创建引用 ResourceFlavor 的 ClusterQueue {#2-create-a-clusterqueue-referencing-the-flavors}

编写引用 ResourceFlavor 的 ClusterQueue 清单文件。应类似如下：

```yaml
# cluster-queue.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {} # match all.
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 9
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 12
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 84Gi
```

`.spec.resourceGroups[*].flavors[*].name`
字段中的风味名称应与前面创建的 ResourceFlavor 名称一致。

注意，`memory` 引用了在[单一风味设置](#single-clusterqueue-and-single-resourceflavor-setup)
中创建的 `default-flavor` 风味。
这意味着你不区分内存来自 `x86` 还是 `arm` 节点。

要创建 ClusterQueue，请运行以下命令：

```shell
kubectl apply -f cluster-queue.yaml
```

## 多 ClusterQueue 与借用 cohort {#multiple-clusterqueues-and-borrowing-cohorts}

两个或多个 ClusterQueue 可以在同一 [cohort](/zh-CN/docs/concepts/cluster_queue#cohort)
中相互借用未使用的配额。

通过以下示例，你可以建立一个包含 ClusterQueue `team-a-cq` 和 `team-b-cq` 的 cohort `team-ab`。

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
        borrowingLimit: 6
      - name: "memory"
        nominalQuota: 36Gi
        borrowingLimit: 24Gi
```

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {}
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 12
      - name: "memory"
        nominalQuota: 48Gi
```

注意，ClusterQueue `team-a-cq` 还定义了 [borrowingLimit](/zh-CN/docs/concepts/cluster_queue#borrowingLimit)。
这限制了 ClusterQueue 从 cohort 借用未使用配额的能力，
最多只能借用到配置的 `borrowingLimit`，即使配额完全未被使用。

要创建这些 ClusterQueue，
请保存上述清单并运行以下命令：

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml
```

## 多个 ClusterQueue 包含专用和回退资源类型 {#multiple-clusterqueue-with-dedicated-and-fallback-flavors}

即使 ClusterQueue 某个风味的 nominalQuota 为零，
也可以从 [cohort](/zh-CN/docs/concepts/cluster_queue#cohort)
借用资源。这允许你为某个风味分配专用配额，并在需要时回退到与其他租户共享的风味配额。

这种设置可以通过为每个租户创建一个 ClusterQueue，
并为共享资源创建一个额外的 ClusterQueue 实现。
例如，两个租户的清单如下：

```yaml
# team-a-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-a-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 9
        borrowingLimit: 0
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 0
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 36Gi
```

```yaml
# team-b-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "team-b-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "arm"
      resources:
      - name: "cpu"
        nominalQuota: 12
        borrowingLimit: 0
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 0
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 48Gi
```

```yaml
# shared-cq.yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "shared-cq"
spec:
  namespaceSelector: {} # match all.
  cohort: "team-ab"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "x86"
      resources:
      - name: "cpu"
        nominalQuota: 6
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 24Gi
```

请注意以下设置：

- `team-a-cq` 和 `team-b-cq` 为 `arm` 风味定义了 `borrowingLimit: 0`，
  因此它们不能相互借用该风味。
- `team-a-cq` 和 `team-b-cq` 为 `x86` 风味定义了 `nominalQuota: 0`，
  因此它们没有该风味的专用配额，
  只能从 `shared-cq` 借用。

要创建这些 ClusterQueue，
请保存上述清单并运行以下命令：

```shell
kubectl apply -f team-a-cq.yaml -f team-b-cq.yaml -f shared-cq.yaml
```

## 在配额管理中排除任意资源 {#exclude-arbitrary-resources-in-the-quota-management}
默认情况下，管理员必须在 ClusterQueue 的 `.spec.resourceGroups[*]` 中指定 Pod 所需的所有资源。
如果你希望在 ClusterQueue 配额管理和准入过程中排除某些资源，可以在 Kueue 配置中以集群级别设置资源前缀。

请按照[使用自定义配置的安装说明](/zh-CN/docs/installation#install-a-custom-configured-released-version)
操作，并在配置中添加如下字段：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
resources:
  excludeResourcePrefixes:
  - "example.com"
```

## 配额管理的资源转换 {#transform-resources-for-quota-management}

{{< feature-state state="beta" for_version="v0.10" >}}
{{% alert title="注意" color="primary" %}}

`ConfigurableResourceTransformation` 是一个 Beta 特性，默认启用。

你可以通过设置 `ConfigurableResourceTransformation` 特性开关来禁用它。详细的特性开关配置请查阅[安装指南](/zh-CN/docs/installation/#change-the-feature-gates-configuration)。
{{% /alert %}}

管理员可以自定义 Pod 请求的资源是如何转换成 Workload 的资源请求的。
这样，ClusterQueue 在执行准入（admission）和配额（quota）计算时，
可以基于转换后的资源需求进行，
而无需更改 Workload
在被 ClusterQueue 接受后、其创建的 Pod 向 Kubernetes Scheduler 所呈现的资源请求与限制。
这种自定义通过在 Kueue 配置中指定 资源转换规则 来实现，
并作为集群级别的设置生效。

支持的转换允许通过乘以缩放因子，将输入资源映射为一个或多个输出资源。
输入资源可以保留（默认）或从转换后的资源中移除。
如果未为输入资源定义转换，则保持不变。

请按照[使用自定义配置的安装说明](/zh-CN/docs/installation#install-a-custom-configured-released-version)
操作，并在 Kueue 配置中添加如下字段：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
resources:
  transformations:
  - input: example.com/gpu-type1:
    strategy: Replace
    outputs:
      example.com/gpu-memory: 5Gi
      example.com/credits: 10
  - input: example.com/gpu-type2:
    strategy: Replace
    outputs:
      example.com/gpu-memory: 20Gi
      example.com/credits: 40
  - input: cpu
    strategy: Retain
    outputs:
      example.com/credits: 1
```

有了此配置，Pod 请求如下：
```yaml
    resources:
      requests:
        cpu: 1
        memory: 100Gi
      limits:
        example.com/gpu-type1: 2
        example.com/gpu-type2: 1
```

Workload 获得的有效资源请求为：

```yaml
    resources:
      requests:
        cpu: 1
        memory: 100Gi
        example.com/gpu-memory: 30Gi
        example.com/credits: 61
```
