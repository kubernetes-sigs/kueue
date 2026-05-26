---
title: "配置外部证书管理器"
date: 2025-03-14
weight: 1
description: >
  证书管理器支持
---

此页面展示了如何使用第三方证书颁发机构管理证书的解决方案，如 Cert Manager。

该页面适用于[批处理管理员](/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- 一个 Kubernetes 集群正在运行。
- kubectl 命令行工具与你的集群通信。
- [Kueue 已安装](/zh-CN/docs/installation)。
- Cert Manager 已[安装](https://cert-manager.io/docs/installation/)

Kueue 支持 Kustomize 或通过 Helm chart 进行安装。

### 内部证书管理

在所有情况下，如果想要使用 CertManager，必须关闭 Kueue 的内部证书管理。

### 使用 Kustomize 安装

1. 在 kueue 配置中，将 `internalCertManagement.enable` 设置为 `false`。
2. 在 `config/default/kustomization.yaml` 中注释掉 `internalcert` 文件夹。
3. 启用 `config/default/kustomization.yaml` 中的 `cert-manager`，并取消注释所有包含 'CERTMANAGER' 的部分。

### Helm 安装

Kueue 也支持通过 Helm values 集成 Cert Manager。
当 `enableCertManager` 设置为 `true` 时，chart 会在生成的配置中自动关闭
Kueue 的内部证书管理。

1. 在你的 `values.yaml` 文件中，将 `enableCertManager` 设置为 `true`。
2. 默认情况下，chart 会创建一个自签名 `Issuer`。
3. 如果你想复用已有的 `Issuer` 或 `ClusterIssuer`，请设置 `certManager.issuerRef`。
4. 如果你引用的是命名空间级别的 `Issuer`，它必须已经存在于 Helm release
   所在的命名空间中。
5. 所引用的 issuer 必须提供 Kueue 的 cert-manager 集成所需的 CA 数据，
   包括生成的 Secret 中的 `ca.crt`，以及用于 webhook 和 visibility API
   注入的 CA bundle。

例如，使用一个已有的 `ClusterIssuer`：

```yaml
enableCertManager: true
certManager:
  issuerRef:
    group: cert-manager.io
    kind: ClusterIssuer
    name: my-cluster-issuer
```
