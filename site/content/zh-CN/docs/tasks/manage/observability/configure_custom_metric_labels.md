---
title: "配置自定义指标标签"
linkTitle: "自定义指标标签"
date: 2026-03-10
weight: 4
description: >
  从 Kubernetes 元数据向 Kueue ClusterQueue、LocalQueue 和 Cohort 指标添加自定义 Prometheus 标签。
---

本页面向你展示如何配置自定义指标标签，这些标签将 Kubernetes 标签或注解从
ClusterQueue、LocalQueue 和 Cohort 提升为额外的 Prometheus 指标标签维度。

本页面的目标受众是[批处理管理员](/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- 一个 Kubernetes 集群正在运行。
- kubectl 命令行工具能够与你的集群通信。
- [Kueue 已安装](/docs/installation)。
- Prometheus 已[设置](/docs/tasks/manage/observability/setup_prometheus)以抓取 Kueue 指标。

## 配置自定义标签

自定义指标标签需要 `CustomMetricLabels` 特性门控，它是 Alpha 级别且默认禁用。
关于如何启用特性门控的详情，请参见[安装](/docs/installation/#change-the-feature-gates-configuration)。

在 Kueue 控制器配置中添加一个 `metrics.customLabels` 部分。
每个条目定义了 ClusterQueue、LocalQueue 和 Cohort 指标上的一个额外 Prometheus 标签维度。

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  CustomMetricLabels: true
metrics:
  customLabels:
    - name: team
    - name: cost_center
      sourceLabelKey: "billing/cost-center"
    - name: budget
      sourceAnnotationKey: "billing.example.com/budget"
```

### 字段参考

| 字段                | 说明 |
|----------------------|-------------|
| `name`               | Prometheus 标签的后缀。Kueue 自动添加前缀 `custom_` （例如 `name: team` → 标签 `custom_team`）。必须遵循 Prometheus 标签命名规则：`[a-zA-Z_][a-zA-Z0-9_]*`。 |
| `sourceLabelKey`     | 从中读取值的 Kubernetes 标签键。必须是有效的 Kubernetes 合格名称。与 `sourceAnnotationKey` 互斥。如果两者都未指定，则默认为 `name`。 |
| `sourceAnnotationKey`| 从中读取值的 Kubernetes 注解键。必须是有效的 Kubernetes 合格名称。与 `sourceLabelKey` 互斥。 |

最多可以配置 **8** 个自定义标签。

### 值如何解析

对于每个 ClusterQueue、LocalQueue 或 Cohort，Kueue 读取指定的 Kubernetes
标签或注解的值。如果对象没有该标签/注解，则使用空字符串作为值。

## 示例：按团队标记

使用上述配置和 `name: team`：

1. 创建一个带有 `team` 标签的 ClusterQueue：

    ```yaml
    apiVersion: kueue.x-k8s.io/v1beta2
    kind: ClusterQueue
    metadata:
      name: engineering-cq
      labels:
        team: platform
    spec:
      resourceGroups:
        - coveredResources: ["cpu"]
          flavors:
            - name: default
              resources:
                - name: cpu
                  nominalQuota: 10
    ```

2. 按团队筛选查询指标：

    ```promql
    kueue_pending_workloads{custom_team="platform"}
    kueue_admitted_workloads_total{custom_team="platform"}
    ```

{{% alert title="基数考虑" color="warning" %}}
每个标签值的唯一组合在 Prometheus 中创建一个单独的时间序列。
保持不同的值数量较低（例如团队名称、成本中心），以避免过高的基数。
避免使用高基数的值，如用户 ID 或时间戳。
{{% /alert %}}

## 下一步

- 查看 [Prometheus 指标](/docs/reference/metrics)，获取 Kueue 指标的完整列表。
- 查看[常用 Grafana 查询](/docs/tasks/manage/observability/common_grafana_queries)，
  获取用于在 Grafana 中监控 Kueue 的 PromQL 查询。
