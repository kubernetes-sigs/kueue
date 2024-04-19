---
title: "Kubectl Kueue Plugin"
date: 2024-04-30
description: >
  The kubectl-kueue plugin, kueuectl, allows you to create kueue resources such as localqueue.
---

## Installing From Release Binaries

### 1. Download the latest release:

On Linux:
```shell
# For AMD64 / x86_64
curl -LO "https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/bin/linux/amd64/kubectl-kueue"
# For ARM64
curl -LO "https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/bin/linux/arm64/kubectl-kueue"
```

On Mac:
```shell
# For Intel Macs
curl -LO "https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/bin/darwin/amd64/kubectl-kueue"
# For M1 / ARM Macs
curl -LO "https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/bin/darwin/arm64/kubectl-kueue"
```

### 2. Make the kubectl-kueue binary executable.

```shell
chmod +x ./kubectl-kueue
```

### 3. Move the kubectl binary to a file location on your system PATH.

```shell
sudo mv ./kubectl-kueue /usr/local/bin/kubectl-kueue
```

## Installing From Source

```bash
make kueuectl
sudo mv ./bin/kubectl-kueue /usr/local/bin/kubectl-kueue
```

## Kueuectl

Additionally, you can create an alias `kueuectl` to allow shorter syntax.

```bash
echo 'alias kueuectl="kubectl kueue"' >> ~/.bashrc
# Or if you are using ZSH
echo 'alias kueuectl="kubectl kueue"' >> ~/.zshrc
```

## Syntax

Use the following syntax to run `kubectl kueue` commands from your terminal window:

```shell
kubectl kueue [command] [TYPE] [NAME] [flags]
```

or with shorter syntax `kueuectl`:

```shell
kueuectl [command] [TYPE] [NAME] [flags]
```

You can run `kubectl kueue help` in the terminal to get the full list of commands, along with all possible flags.


## Operations

The following table includes short descriptions and the general syntax for all of the kueuectl operations:

| Operation | Syntax                       | Description                                             |
|--------|------------------------------|---------------------------------------------------------|
| create | kubectl kueue create [TYPE] [NAME] [flags] | Create resource. | 


## Examples: Common operations

```shell
# Create a local queue 
kubectl kueue create localqueue my-local-queue -c my-cluster-queue
```
