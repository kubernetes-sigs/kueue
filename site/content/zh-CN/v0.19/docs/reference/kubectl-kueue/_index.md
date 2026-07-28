---
title: "Kubectl Kueue 插件"
linkTitle: "Kubectl Kueue 插件"
date: 2024-07-02
weight: 10
description: >
  kubectl-kueue 插件，即 kueuectl，允许你列出、创建、恢复和停止 Kueue 资源，如 resourceflavor、clusterqueue、localqueue 和 workload。
---

## 语法 {#syntax}

使用以下语法从终端窗口运行 `kubectl kueue` 命令：

```shell
kubectl kueue [OPERATION] [TYPE] [NAME] [flags]
```

或使用更短的语法 `kueuectl`：

```shell
kueuectl [OPERATION] [TYPE] [NAME] [flags]
```

你可以在终端中运行 `kubectl kueue help` 来获取完整的命令列表以及所有可能的标志。