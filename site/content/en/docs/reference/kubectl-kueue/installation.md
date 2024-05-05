---
title: "Installation"
linkTitle: "Installation"
date: 2024-05-09
weight: 20
description: >
  Installing the kubectl-kueue plugin, kueuectl.
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
