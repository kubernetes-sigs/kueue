---
title: "设置 Prometheus"
linkTitle: "设置 Prometheus"
date: 2026-02-04
weight: 1
description: >
  为 Kueue 启用 Prometheus 指标抓取
---

此页面展示了如何设置 Prometheus 以抓取 Kueue 指标。
对于使用 TLS 保护的指标端点，请参阅[使用 TLS 配置 Prometheus](/docs/tasks/manage/productization/prometheus)。

本页面适用于[批处理管理员](/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具可以与你的集群通信。
- [Kueue 已安装](/docs/installation)。
- Prometheus Operator 已[安装](https://prometheus-operator.dev/docs/getting-started/installation/)。

## 1. 设置

选择与你的 Kueue 安装匹配的设置方法。

### 选项 A：Helm

如果你使用 Helm 安装了 Kueue，在你的 `values.yaml` 中启用 Prometheus 抓取：

```yaml
enablePrometheus: true
```

然后升级你的 Helm 版本：

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --namespace kueue-system \
  -f values.yaml
```

### 选项 B：清单文件

如果你使用 kubectl 安装了 Kueue 并附带了发布清单文件，请应用 Prometheus ServiceMonitor：

```bash
VERSION={{< param "version" >}}
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/${VERSION}/prometheus.yaml
```

## 2. 验证指标

1. 检查 ServiceMonitor 是否已创建：

   ```bash
   kubectl get servicemonitor -n kueue-system
   ```

   You should see `kueue-controller-manager-metrics-monitor` listed.

2. 在 Prometheus 用户界面中，转到“状态” > “目标运行状况”（或导航至 `/targets`），
   并验证 `kueue-system/kueue-controller-manager-metrics-monitor` 显示为 `UP`。

3. 在 Prometheus 用户界面中运行测试查询：

   ```promql
   kueue_admitted_workloads_total
   ```

   如果 Kueue 已处理工作负载，你应该可以看到集群队列的数据点。

## 3. 启用可选指标

默认情况下，Kueue 不会导出 ClusterQueues 的资源级别指标。
要启用如 `kueue_cluster_queue_resource_usage` 和 `kueue_cluster_queue_nominal_quota` 等指标，

在 Kueue 配置中设置 `enableClusterQueueResources: true`。

编辑 `kueue-manager-config` ConfigMap：

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-manager-config
  namespace: kueue-system
data:
  controller_manager_config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    metrics:
      bindAddress: :8443
      enableClusterQueueResources: true
    # ... other configuration
```

重启控制器以使更改生效：

```bash
kubectl rollout restart deployment/kueue-controller-manager -n kueue-system
```

参阅 [Prometheus 指标](/docs/reference/metrics#optional-metrics)获取可选指标的完整列表。

## 接下来是什么

- 参阅[常用 Grafana 查询](/docs/tasks/manage/observability/common_grafana_queries)
  获取用于在 Grafana 中监控 Kueue 的 PromQL 查询。
- 参阅[使用 TLS 配置 Prometheus](/docs/tasks/manage/productization/prometheus)
  获取使用 cert-manager 进行高级 TLS 配置的信息。
- 参阅[设置开发监控](/docs/tasks/dev/setup_dev_monitoring)获取带有
  Prometheus 的本地开发环境设置指南。