# Init

For the purpose of this guide [kubebuilder](https://book.kubebuilder.io/) is used to bootstrap the Kubernetes operator that will run the Demo Admission Check Controller.

Check the kubebuilder [quick-start guide](https://book.kubebuilder.io/quick-start) for details.

Choose a domain and repo for the project, for the purpose of this demo the project's domain is `demo-acc.experimental.kueue.x-k8s.io` and the repo `sigs.k8s.io/kueue/cmd/experimental/demo-acc`.

## Init the project

```bash
kubebuilder init --domain demo-acc.experimental.kueue.x-k8s.io --repo sigs.k8s.io/kueue/cmd/experimental/demo-acc 
```

