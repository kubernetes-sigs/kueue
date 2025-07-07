---
title: "开启 pprof 端点"
date: 2023-07-21
weight: 3
description: >
  为 Kueue 控制器管理器启用 pprof 端点。
---

本页面向你展示如何为 Kueue 控制器管理器开启 pprof 端点。

本页面的目标读者是[批处理管理员](/zh-CN/docs/tasks#batch-administrator)。

## 开始之前

确保满足以下条件：

- Kubernetes 集群正在运行。
- kubectl 命令行工具能够与你的集群进行通信。
- [Kueue 已安装](/zh-cn/docs/installation)。

## 开启 pprof 端点

{{< feature-state state="stable" for_version="v0.5" >}}

要开启 pprof 端点，
你需要在[管理器的配置](/zh-cn/docs/installation/#install-a-custom-configured-released-version)中设置 `pprofBindAddress`。

在 Kubernetes 中访问 pprof 端口最简单的方法是使用 `port-forward` 命令。

1. 运行以下命令获取运行 Kueue 的 Pod 名称：

```shell
kubectl get pod -n kueue-system
NAME                                        READY   STATUS    RESTARTS   AGE
kueue-controller-manager-769f96b5dc-87sf2   2/2     Running   0          45s
```

2. 运行以下命令以启动到本地主机的端口转发：

```shell
kubectl port-forward kueue-controller-manager-769f96b5dc-87sf2 -n kueue-system 8083:8083
Forwarding from 127.0.0.1:8083 -> 8083
Forwarding from [::1]:8083 -> 8083
```

现在，HTTP 端点可以作为一个本地端口来使用。

要了解如何使用暴露的端点，请参阅
[pprof 基本用法](https://github.com/google/pprof#basic-usage)和[示例](https://pkg.go.dev/net/http/pprof#hdr-Usage_examples)。
