---
title: "配置 Prometheus"
date: 2025-03-14
weight: 2
description: >
  Prometheus 支持
---

此页面展示了如何配置 Kueue 以使用 Prometheus 指标。

此页面适用于[批处理管理员](/zh-CN/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- 一个 Kubernetes 集群正在运行。
- kubectl 命令行工具可以与集群通信。
- [Kueue 已安装](/zh-cn/docs/installation)。
- Prometheus 已[安装](https://prometheus-operator.dev/docs/getting-started/installation/)
- Cert Manager 已[安装](https://cert-manager.io/docs/installation/)（可选地）

Kueue 支持 Kustomize 或通过 Helm chart 安装。

### Kustomize 安装

1. 在 `config/default/kustomization.yaml` 中启用 `prometheus`，并取消注释中所有包含 'PROMETHEUS' 的部分。

#### 使用证书的 Kustomize Prometheus

如果你想为指标端点启用 TLS 验证，请遵循以下步骤。

1. 在 Kueue 配置中将 `internalCertManagement.enable` 设置为 `false`。
2. 在 `config/default/kustomization.yaml` 中注释掉 `internalcert` 文件夹。
3. 在 `config/default/kustomization.yaml` 中启用 `cert-manager`，并取消注释所有包含 'CERTMANAGER' 的部分。
4. 要启用带有 TLS 保护的安全指标，请取消注释所有包含 'PROMETHEUS-WITH-CERTS' 的部分。

### Helm 安装

#### Prometheus 安装

Kueue 也支持通过 Helm 部署 Prometheus。

1. 在你的 `values.yaml` 文件中将 `enablePrometheus` 设置为 true。

#### 使用证书的 Helm Prometheus

如果你想使用外部证书保护指标端点：

1. 在 kueue 配置中禁用内部证书管理（更多详情参见[自定义配置](https://kueue.sigs.k8s.io/zh-cn/docs/installation/#install-a-custom-configured-released-version)）。
2. 将 `enableCertManager` 和 `enablePrometheus` 都设置为 true。
3. 提供 `tlsConfig` 的值，见下面的例子：

在 Helm Chart 中，你的 `tlsConfig` 示例可以如下所示：

```yaml
...
metrics:
  prometheusNamespace: monitoring
  # serviceMonitor 的 tls 配置
  serviceMonitor:
    tlsConfig:
      serverName: kueue-controller-manager-metrics-service.kueue-system.svc
      ca:
        secret:
          name: kueue-metrics-server-cert
          key: ca.crt
      cert:
        secret:
          name: kueue-metrics-server-cert
          key: tls.crt
      keySecret:
        name: kueue-metrics-server-cert
        key: tls.key
```

这些 Secret 必须引用 cert-manager 生成的 Sercret。
