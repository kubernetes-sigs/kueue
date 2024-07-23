# Installation

Installing the `kubectl-kjob` plugin, `kjobctl`.

## Installing From Release Binaries

### 1. Install Kubernetes CRDs

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.8.0/kubectl-kjob-manifests.yaml
```

### 2. Download the latest release:

On Linux:
```shell
# For AMD64 / x86_64
[ $(uname -m) = x86_64 ] && curl -Lo ./kubectl-kjob https://github.com/kubernetes-sigs/kjob/releases/download/v0.8.0/kubectl-kjob-linux-amd64
# For ARM64
[ $(uname -m) = aarch64 ] && curl -Lo ./kubectl-kjob https://github.com/kubernetes-sigs/kjob/releases/download/v0.8.0/kubectl-kjob-linux-arm64
```

On Mac:
```shell
# For Intel Macs
[ $(uname -m) = x86_64 ] && curl -Lo ./kubectl-kjob https://github.com/kubernetes-sigs/kjob/releases/download/v0.8.0/kubectl-kjob-darwin-amd64
# For M1 / ARM Macs
[ $(uname -m) = arm64 ] && curl -Lo ./kubectl-kjob https://github.com/kubernetes-sigs/kjob/releases/download/v0.8.0/kubectl-kjob-darwin-arm64
```

### 3. Make the kubectl-kjob binary executable.

```shell
chmod +x ./kubectl-kjob
```

### 4. Move the kubectl binary to a file location on your system PATH.

```shell
sudo mv ./kubectl-kjob /usr/local/bin/kubectl-kjob
```

## Installing From Source

```bash
make kubectl-kjob
sudo mv ./bin/kubectl-kjob /usr/local/bin/kubectl-kjob
```

## Kjobctl

Additionally, you can create an alias `kjobctl` to allow shorter syntax.

```bash
echo 'alias kjobctl="kubectl kjob"' >> ~/.bashrc
# Or if you are using ZSH
echo 'alias kjobctl="kubectl kjob"' >> ~/.zshrc
```

## Autocompletion

```bash
echo '[[ $commands[kubectl-kjob] ]] && source <(kubectl-kjob completion bash)' >> ~/.bashrc
# Or if you are using ZSH
echo '[[ $commands[kubectl-kjob] ]] && source <(kubectl-kjob completion zsh)' >> ~/.zshrc

cat <<EOF >kubectl_complete-kjob
#!/usr/bin/env sh

# Call the __complete command passing it all arguments
kubectl kjob __complete "\$@"
EOF

chmod u+x kubectl_complete-kjob
sudo mv kubectl_complete-kjob /usr/local/bin/kubectl_complete-kjob
```

## See Also

* [overview](_index.md)	 - `kubectl-kjob` plugin, `kjobctl` overview.
* [commands](commands/kjobctl.md)	 - Full list of commands, along with all possible flags.