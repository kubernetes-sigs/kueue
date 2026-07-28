---
title: "Kubectl Kueue Plugin"
linkTitle: "Kubectl Kueue Plugin"
date: 2024-07-02
weight: 10
description: >
  The kubectl-kueue plugin, kueuectl, allows you to list, create, resume and stop kueue resources such as resourceflavor, clusterqueues, localqueues and workloads.
---

## Syntax

Use the following syntax to run `kubectl kueue` commands from your terminal window:

```shell
kubectl kueue [OPERATION] [TYPE] [NAME] [flags]
```

or with shorter syntax `kueuectl`:

```shell
kueuectl [OPERATION] [TYPE] [NAME] [flags]
```

You can run `kubectl kueue help` in the terminal to get the full list of commands, along with all possible flags.