# Init

For the purpose of this guide [kubebuilder](https://book.kubebuilder.io/) is used to bootstrap the Kubernetes operator that will run the Demo Admission Check Controller.

Check the kubebuilder [quick-start guide](https://book.kubebuilder.io/quick-start) for details.

Choose a domain and repo for the project, for the purpose of this demo the project's domain is `demo-acc.experimental.kueue.x-k8s.io` and the repo `sigs.k8s.io/kueue/cmd/experimental/demo-acc`.

## Init the project

```bash
kubebuilder init --domain demo-acc.experimental.kueue.x-k8s.io --repo sigs.k8s.io/kueue/cmd/experimental/demo-acc 
```

## Scaffold the controllers

Note: Kubebuilder should have better support for scaffolding controllers for external API resources in the near future, follow [this pr](https://github.com/kubernetes-sigs/kubebuilder/pull/4171) for details.

```bash
kubebuilder create api --group replace-me --version v1beta1 --kind AdmissionCheck --controller=true  --resource=false
kubebuilder create api --group replace-me --version v1beta1 --kind Workload --controller=true  --resource=false
```

Replace the dummy group with kueue's group.

```bash
sed -i 's/groups=replace-me.\+,resources/groups=kueue.x-k8s.io,resources/g' internal/controller/*.go
yq -i '(.resources[]|select(.group == "replace-me")) |= .domain="x-k8s.io" '  PROJECT
yq -i '(.resources[]|select(.group == "replace-me")) |= .group="kueue" '  PROJECT
```

Regenerate the code

```bash
make genetrate manifests
```

Note: The generated `internal/controller/suite_test.go` is missing the "context" import it should be added.
