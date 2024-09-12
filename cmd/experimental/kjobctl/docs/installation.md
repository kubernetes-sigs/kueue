# Installation

Installing the `kubectl-kjob` plugin, `kjobctl`.

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