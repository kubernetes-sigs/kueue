---
title: "安装"
linkTitle: "安装"
date: 2024-05-09
weight: 20
description: >
  安装 kubectl-kueue 插件，kueuectl。
---

## 通过 Krew 安装 {#installing_via_krew}

```shell
kubectl krew install kueue
```

## 从发布版本二进制文件安装 {#installing_from_release_binaries}

### 1. 下载最新版本：{#download_the_latest_release}

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

### 2. 使 kubectl-kueue 二进制文件可执行。 {#make_the_kubectl-kueue_binary_executable}

```shell
chmod +x ./kubectl-kueue
```

### 3. 将 kubectl 二进制文件移动到系统 PATH 上的文件位置。{#move_the_kubectl_binary_to_a_file_location_on_your_system_path}

```shell
sudo mv ./kubectl-kueue /usr/local/bin/kubectl-kueue
```

## 从源码安装 {#installing_from_source}

```bash
make kueuectl
sudo mv ./bin/kubectl-kueue /usr/local/bin/kubectl-kueue
```

## Kueuectl {#kueuectl}

此外，您可以创建别名 `kueuectl` 以允许更短的语法。

{{< tabpane lang=shell persist=disabled >}}
{{< tab header="Bash" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.bashrc{{< /tab >}}
{{< tab header="Zsh" >}}echo 'alias kueuectl="kubectl kueue"' >> ~/.zshrc{{< /tab >}}
{{< /tabpane >}}

## 自动补全 {#autocompletion}

{{< tabpane lang=shell persist=disabled >}}
{{< tab header="Bash" >}}echo '[[ $commands[kubectl-kueue] ]] && source <(kubectl-kueue completion bash)' >> ~/.bashrc{{< /tab >}}
{{< tab header="Zsh" >}}echo '[[ $commands[kubectl-kueue] ]] && source <(kubectl-kueue completion zsh)' >> ~/.zshrc{{< /tab >}}
{{< /tabpane >}}

```bash
cat <<EOF >kubectl_complete-kueue
#!/usr/bin/env sh

# 调用 __complete 命令并传递所有参数
kubectl kueue __complete "\$@"
EOF

chmod u+x kubectl_complete-kueue
sudo mv kubectl_complete-kueue /usr/local/bin/kubectl_complete-kueue
```