---
title: "Installation"
linkTitle: "Installation"
date: 2024-05-09
weight: 20
description: >
  Installing the kubectl-kueue plugin, kueuectl.
---

## Installing via Krew

```shell
kubectl krew install kueue
```

## Installing From Release Binaries

### 1. Download the latest release:

On Linux:

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="AMD64 / x86_64"  >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-linux-amd64{{< /tab >}}
{{< tab header="ARM64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-linux-arm64{{< /tab >}}
{{< /tabpane >}}

On Mac:

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="AMD64 / x86_64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-darwin-amd64{{< /tab >}}
{{< tab header="ARM64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-darwin-arm64{{< /tab >}}
{{< /tabpane >}}

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

{{< tabpane lang=shell persist=disabled >}}
{{< tab header="Bash" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.bashrc{{< /tab >}}
{{< tab header="Zsh" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.zshrc{{< /tab >}}
{{< /tabpane >}}

## Autocompletion

{{< tabpane lang=shell persist=disabled >}}
{{< tab header="Bash" >}}echo '[[ $commands[kubectl-kueue] ]] && source <(kubectl-kueue completion bash)' >> ~/.bashrc{{< /tab >}}
{{< tab header="Zsh" >}}echo '[[ $commands[kubectl-kueue] ]] && source <(kubectl-kueue completion zsh)' >> ~/.zshrc{{< /tab >}}
{{< /tabpane >}}

```bash
cat <<EOF >kubectl_complete-kueue
#!/usr/bin/env sh

# Call the __complete command passing it all arguments
kubectl kueue __complete "\$@"
EOF

chmod u+x kubectl_complete-kueue
sudo mv kubectl_complete-kueue /usr/local/bin/kubectl_complete-kueue
```