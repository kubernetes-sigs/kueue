# kjobctl
ML/AI/Batch Jobs Made Easy

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Getting Started

### Prerequisites
- go version v1.22.3+
- kubectl version v1.27+.
- Access to a Kubernetes v1.27+ cluster.

### To Install

**Install the CRDs into the cluster:**

```sh
make install
```

**Install `kubectl kjob` plugin:**

```sh
make kubectl-kjob
sudo cp ./bin/kubectl-kjob /usr/local/bin/kubectl-kjob
```

**Additionally, you can create an alias `kjobctl` to allow shorter syntax:**

```sh
echo 'alias kjobctl="kubectl kjob"' >> ~/.bashrc
# Or if you are using ZSH
echo 'alias kjobctl="kubectl kjob"' >> ~/.zshrc
```

**Autocompletion:**

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

### To Uninstall

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**Delete `kubectl kjob` plugin:**

```sh
sudo rm /usr/local/bin/kubectl-kjob
```

**NOTE:** Run `make help` for more information on all potential `make` targets

## License

Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

