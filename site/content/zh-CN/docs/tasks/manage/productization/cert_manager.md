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

Kueue 也支持为启用 Cert Manager 提供了可选的 helm 值。

1. 在 Kueue 配置中禁用 `internalCertManager`。
2. 在你的 `values.yaml` 文件中，将 `enableCertManager` 设置为 true。
