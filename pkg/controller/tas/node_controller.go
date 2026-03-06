/*
Copyright The Kubernetes Authors.

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

package tas

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	utilnode "sigs.k8s.io/kueue/pkg/util/node"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

type NodeTASCache interface {
	AddOrUpdateNode(node *corev1.Node)
	DeleteNode(nodeName string)
}

type NodeReconciler struct {
	client      client.Client
	tasCache    NodeTASCache
	roleTracker *roletracker.RoleTracker
}

func newNodeReconciler(c client.Client, tasCache NodeTASCache, roleTracker *roletracker.RoleTracker) *NodeReconciler {
	return &NodeReconciler{
		client:      c,
		tasCache:    tasCache,
		roleTracker: roleTracker,
	}
}

// setupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) setupWithManager(mgr ctrl.Manager, cfg *configapi.Configuration) (string, error) {
	return TASNodeController, ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Named("node_controller").
		WithOptions(controller.Options{
			NeedLeaderElection:      ptr.To(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[corev1.SchemeGroupVersion.WithKind("Node").GroupKind().String()],
		}).
		WithLogConstructor(roletracker.NewLogConstructor(r.roleTracker, TASNodeController)).
		Complete(core.WithLeadingManager(mgr, r, &corev1.Pod{}, cfg))
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Node")

	node := &corev1.Node{}
	err := r.client.Get(ctx, req.NamespacedName, node)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	if err != nil || !utilnode.IsActive(node) {
		r.tasCache.DeleteNode(req.Name)
		return ctrl.Result{}, nil
	}

	r.tasCache.AddOrUpdateNode(node)

	return ctrl.Result{}, nil
}
