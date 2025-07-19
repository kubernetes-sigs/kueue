---
title: "Grafana 中监控待处理的工作负载"
date: 2025-06-12
weight: 20
description: >
  使用 Grafana 中的 **VisibilityOnDemand** 特性监控待处理的工作负载。
---

本指南解释了如何使用 `VisibilityOnDemand` 特性在 Grafana 中监控待处理的工作负载。

本文的受众是[批处理管理员](/zh-CN/docs/tasks#batch-administrator)，
用于 ClusterQueue 可见性，以及[批处理用户](/zh-cn/docs/tasks#batch-user)用于 LocalQueue 可见性。

## 开始之前

确保满足以下条件：

- 正在运行一个 Kubernetes 集群。
- 已安装 [Kueue](/zh-CN/docs/installation)
- 已安装 [Kube-prometheus operator](https://github.com/prometheus-operator/kube-prometheus/blob/main/README.md#quickstart)
  版本 v0.15.0 或更新版本。
- 已启用 [VisibilityOnDemand](/zh-CN/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/#monitor-pending-workloads-on-demand) 特性。

## 配置 Grafana 监控待处理工作负载   {#setting-up-grafana-for-pending-workloads}

### 步骤 1：配置集群权限

为了启用可见性，为 `ClusterQueue` 或 `LocalQueue` 创建 `ClusterRole` 和 `ClusterRoleBinding`：

- 对于 `ClusterQueue` 可见性：

{{< include "examples/visibility/grafana-cluster-queue-reader.yaml" "yaml" >}}

- 对于 `LocalQueue` 可见性：

{{< include "examples/visibility/grafana-local-queue-reader.yaml" "yaml" >}}

应用适当的配置：

```shell
kubectl apply -f <filename>.yaml
```

### 步骤 2：生成服务账号令牌

为 Grafana 创建身份验证令牌：

```shell
TOKEN=$(kubectl create token default -n default)
echo $TOKEN
```

保存令牌，以便在步骤 5 中使用。

### 步骤 3：为 Grafana 设置端口转发

本地访问 Grafana ：

```shell
kubectl port-forward -n monitoring service/grafana 3000:3000
```

Grafana 现已在 [http://localhost:3000](http://localhost:3000) 可用。

### 步骤 4：安装 Infinity 插件

1. 打开 Grafana，访问 [http://localhost:3000](http://localhost:3000)。
2. 登录（默认凭据：admin/admin）。
3. 转到 `Connections` > `Add new connection`。
4. 搜索 `Infinity` 并点击 `Install`。

### 步骤 5：配置 Infinity 数据源

1. 转到 `Connections` > `Data sources` 并点击 `+ Add new data source`。
2. 选择 `Infinity`。
3. 配置数据源：
    - 认证：设置 `Bearer Token` 为步骤 2 中生成的令牌。
    - 网络：启用 `Skip TLS Verify`。
    - 安全性：添加 `https://kubernetes.default.svc` 到允许的主机，并将 `Query security` 设置为 `Allowed`。
4. 点击 `Save & test` 以验证配置。

### 步骤 6：导入待处理工作负载仪表板

1. 下载适当的仪表板 JSON：
    - [ClusterQueue 可视化](examples/visibility/pending-workloads-for-cluster-queue-visibility-dashboard.json)。
    - [LocalQueue 可视化](examples/visibility/pending-workloads-for-local-queue-visibility-dashboard.json)。
2. 在 Grafana 中，转到 `Dashboards` > `New` > `Import`。
3. 选择 `Upload dashboard JSON` 文件并选择下载的文件。
4. 选择在步骤 5 中配置的 Infinity 数据源。
5. 点击 `Import`。

### 步骤 7：设置 ClusterQueue

要配置一个基本的 `ClusterQueue`，应用以下内容：

{{< include "examples/admin/single-clusterqueue-setup.yaml" "yaml" >}}

应用配置：

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

### 步骤 8：创建示例工作负载

要向仪表板填充数据，创建示例作业：

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}

多次应用该作业：

```shell
for i in {1..6}; do kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml; done
```

### 步骤 9：查看仪表板

1. 在 Grafana 中，导航到 `Dashboards`。
2. 选择导入的仪表板（例如，“ClusterQueue 可见性的待处理工作负载”）。
3. 确认显示了待处理的工作负载。

![ClusterQueue Visibility Dashboard](/images/pending-workloads-for-cluster-queue-visibility-dashboard.png)

![LocalQueue Visibility Dashboard](/images/pending-workloads-for-local-queue-visibility-dashboard.png)

## 故障排查

### 仪表板中无数据

确保已创建作业并且正确配置了 `Infinity` 数据源。

### 权限错误

验证是否正确应用了 `ClusterRole` 和 `ClusterRoleBinding`。

### 无法访问 Grafana

检查端口转发并确保 Grafana 服务在 monitoring 命名空间中运行。
