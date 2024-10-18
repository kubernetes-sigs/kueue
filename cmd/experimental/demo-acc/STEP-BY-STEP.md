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

## Implement the AdmissionCheck reconciler

Go get `kueue`

```bash
go get sigs.k8s.io/kueue@main
```

In `internal/controller/admissioncheck_controller.go`:

Import the kueue api and some helper packages:

```go
import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)
```

Define the controller's name

```go
const (
	DemoACCName = "experimental.kueue.x-k8s.io/demo-acc"
)

```

Update the reconciler's logic to set active all ACs managed by this controller.

```go
func (r *AdmissionCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var ac kueueapi.AdmissionCheck
	err := r.Get(ctx, req.NamespacedName, &ac)
	if err != nil {
		// Ignore not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ignore if not managed by demo-acc
	if ac.Spec.ControllerName != DemoACCName {
		return ctrl.Result{}, nil
	}

	// For now, we only want to set it active if not already done
	log.V(2).Info("Reconcile AdmissionCheck")
	if !apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueueapi.AdmissionCheckActive) {
		apimeta.SetStatusCondition(&ac.Status.Conditions, metav1.Condition{
			Type:               kueueapi.AdmissionCheckActive,
			Status:             metav1.ConditionTrue,
			Reason:             "Active",
			Message:            "demo-acc is running",
			ObservedGeneration: ac.Generation,
		})
		log.V(2).Info("Update Active condition")
		return ctrl.Result{}, r.Status().Update(ctx, &ac)
	}
	return ctrl.Result{}, nil
}

```

Finally update `SetupWithManager` to be registered `For` Kueue's AdmissionChecks.

```go
func (r *AdmissionCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kueueapi.AdmissionCheck{}).
		Complete(r)
}
```
