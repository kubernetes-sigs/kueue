---
title: "安装"
linkTitle: "安装"
date: 2024-05-09
weight: 20
description: >
  安装 kubectl-kueue 插件，kueuectl。
---

## 通过 Krew 安装

```shell
kubectl krew install kueue
```

## 从发布的二进制文件安装

### 1. 下载最新版本：

在 Linux 上：

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="AMD64 / x86_64"  >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-linux-amd64{{< /tab >}}
{{< tab header="ARM64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-linux-arm64{{< /tab >}}
{{< /tabpane >}}

在 Mac 上：

{{< tabpane lang="shell" persist=disabled >}}
{{< tab header="AMD64 / x86_64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-darwin-amd64{{< /tab >}}
{{< tab header="ARM64" >}}curl -Lo ./kubectl-kueue https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "version" >}}/kubectl-kueue-darwin-arm64{{< /tab >}}
{{< /tabpane >}}

### 2. 使 kubectl-kueue 二进制文件可执行。

```shell
chmod +x ./kubectl-kueue
```

### 3. 将 kubectl 二进制文件移动到系统 PATH 中的某个文件位置。

```shell
sudo mv ./kubectl-kueue /usr/local/bin/kubectl-kueue
```

## 从源代码安装

```bash
make kueuectl
sudo mv ./bin/kubectl-kueue /usr/local/bin/kubectl-kueue
```

## Kueuectl

此外，你还可以创建别名 `kueuectl` 以允许使用更简洁的语法。

{{< tabpane lang=shell persist=disabled >}}
{{< tab header="Bash" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.bashrc{{< /tab >}}
{{< tab header="Zsh" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.zshrc{{< /tab >}}
{{< /tabpane >}}

## 自动补全

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