---
title: "Kubectl Kueue Plugin"
linkTitle: "Kubectl Kueue Plugin"
date: 2024-05-09
weight: 10
description: >
  The kubectl-kueue plugin, kueuectl, allows you to create, resume and stop kueue resources such as localqueue and workload.
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