/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
// update design
// [x] in resource monitor use field Limits corev1.ResourceList instead of custom struct
// iterate thourgh the Limit map, interpret them as integers (otherwise raise an exception). We already know CPU and Memory, but there may be others like GPU
// use .Value() for every value in the dictionary other than CPU, for CPU Millivalue
// change also for Resource monitor
// [x] first check tolerations
// [x] check corev1.Tolerations documentation / source code of Kueue for tolerations methods
// [x] aggregate all the resource request once, then compare with the snapshot
// [x] if one container has request but no limit > use requests > accepts blindly
// [x] if the wl requires a resource not present in the node, reject it

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	// from step by step
	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/experimental/check_node_capacity_before_admission/pkg/evaluator"
	"sigs.k8s.io/kueue/cmd/experimental/check_node_capacity_before_admission/pkg/resource_monitor"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	RequeueAfter = time.Minute
	ReadyMessage = "The workload is now ready"
)

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	SnapshotManager *resource_monitor.SnapshotManager // add a pointer to snapshot manager in the workload reconciler, the snapshot manager runs in a thread
}

// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Workload object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var wl kueueapi.Workload
	err := r.Get(ctx, req.NamespacedName, &wl)
	if err != nil {
		// Ignore not found
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ignore the workloads that don't have quota reservation or are finished
	if !workload.HasQuotaReservation(&wl) || workload.IsFinished(&wl) {
		return ctrl.Result{}, nil
	}

	// Get the states managed by check-node-capacity-before-admission
	managedStatesNames, err := admissioncheck.FilterForController(ctx, r.Client, wl.Status.AdmissionChecks, ACCName)

	if err != nil {
		return ctrl.Result{}, err
	}

	// Ignore if none are managed by check-node-capacity-before-admission
	if len(managedStatesNames) == 0 {
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconcile Workload")

	// Fetch snapshot fo current resources with resource-monitor

	// Log the current snapshot in a better format
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Retrieving Resource Monitoring")
	snapshot := r.SnapshotManager.GetSnapshot()

	var formattedSnapshot strings.Builder
	formattedSnapshot.WriteString("Current Resource Snapshot:\n")
	formattedSnapshot.WriteString(fmt.Sprintf("Last Check: %s\n", snapshot.LastCheck.Format(time.RFC3339)))

	for _, node := range snapshot.Nodes {
		formattedSnapshot.WriteString(fmt.Sprintf("Node Name: %s\n", node.NodeName))
		// formattedSnapshot.WriteString(fmt.Sprintf("Node UUID: %s\n", node.UUID))
		formattedSnapshot.WriteString("Remaining Resources:\n")

		for resource, value := range node.Remaining {
			formattedSnapshot.WriteString(fmt.Sprintf("  %s: %s\n", resource, value.String()))
		}

		// formattedSnapshot.WriteString("Pods:\n")
		// for _, pod := range node.Pods {
		// 	formattedSnapshot.WriteString(fmt.Sprintf("  - Pod Name: %s\n", pod.Name))
		// 	formattedSnapshot.WriteString(fmt.Sprintf("    Namespace: %s\n", pod.Namespace))
		// 	formattedSnapshot.WriteString(fmt.Sprintf("    UUID: %s\n", pod.UUID))
		// 	formattedSnapshot.WriteString("    Resources:\n")
		// 	for resKey, resValue := range pod.Resources {
		// 		formattedSnapshot.WriteString(fmt.Sprintf("      %s: %s\n", resKey, resValue.String()))
		// 	}
		// }
		// formattedSnapshot.WriteString("\n")
	}

	logger.Info(formattedSnapshot.String())

	// If we need to wait
	// if remaining := time.Until(wl.CreationTimestamp.Add(MakeReadyAfter)); remaining > 0 {
	// 	return ctrl.Result{RequeueAfter: remaining}, nil
	// }

	// Schedule the workload
	gang := true
	canSchedule, err := evaluator.Evaluate(ctx, wl, snapshot, gang)
	if err != nil {
		logger.Error(err, "Error during scheduling")
		// return ctrl.Result{}, err
	}

	if !canSchedule {
		logger.Info("Workload cannot be scheduled. Requeuing", "workload", wl.Name, "checkAgainAfterSeconds", RequeueAfter.Seconds())
		return ctrl.Result{RequeueAfter: RequeueAfter}, nil
	}

	// Mark the states 'Ready' if not done already
	needsUpdate := false
	wlPatch := workload.BaseSSAWorkload(&wl)
	for _, name := range managedStatesNames {
		if acs := workload.FindAdmissionCheck(wl.Status.AdmissionChecks, name); acs.State != kueueapi.CheckStateReady {
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, kueueapi.AdmissionCheckState{
				Name:    name,
				State:   kueueapi.CheckStateReady,
				Message: ReadyMessage,
			})
			needsUpdate = true
		} else {
			workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *acs)
		}
	}
	if needsUpdate {
		return ctrl.Result{}, r.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ACCName), client.ForceOwnership)
	}

	// TODO(user): your logic here
	// custom logic by CarloGem

	if needsUpdate {
		// Apply changes to admit the workload
		err = r.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ACCName), client.ForceOwnership)
		if err != nil {
			return ctrl.Result{}, err
		}

		// // Log the current snapshot
		// snapshot := r.SnapshotManager.GetSnapshot()
		// log.Info("Current Resource Snapshot", "snapshot", snapshot)

		// return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		// For().
		For(&kueueapi.Workload{}).
		// Named("workload").
		Complete(r)
}
