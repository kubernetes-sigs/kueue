/*
Copyright 2021 The Kubernetes Authors.

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

package core

import (
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	kueue "sigs.k8s.io/kueue/api/v1alpha1"
)

// WorkloadHandler signals the required controller to reconcile
// ClusterQueue when
// Queue when
type WorkloadHandler struct{}

func (h *WorkloadHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	wl := e.Object.(*kueue.QueuedWorkload)
	if wl.Spec.Admission != nil {
		q.Add(requestForClusterQueue(wl))
	} else {
		q.Add(requestForQueueStatus(wl))
	}
}

func (h *WorkloadHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldW := e.ObjectOld.(*kueue.QueuedWorkload)
	newW := e.ObjectNew.(*kueue.QueuedWorkload)
	// CQ
	q.Add(requestForQueueStatus(newW))
	if oldW.Spec.Admission != nil {
		q.Add(requestForClusterQueue(oldW))
	}
	if newW.Spec.Admission != nil && (oldW.Spec.Admission == nil || newW.Spec.Admission.ClusterQueue != oldW.Spec.Admission.ClusterQueue) {
		q.Add(requestForClusterQueue(newW))
	}
	if newW.Spec.QueueName != oldW.Spec.QueueName {
		q.Add(requestForQueueStatus(oldW))
	}
}

func (h *WorkloadHandler) Delete(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	wl := e.Object.(*kueue.QueuedWorkload)
	if wl.Spec.Admission != nil {
		q.Add(requestForClusterQueue(wl))
	} else {
		q.Add(requestForQueueStatus(wl))
	}
}

func (h *WorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func requestForClusterQueue(w *kueue.QueuedWorkload) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: string(w.Spec.Admission.ClusterQueue),
		},
	}
}

func requestForQueueStatus(w *kueue.QueuedWorkload) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      w.Spec.QueueName,
			Namespace: w.Namespace,
		},
	}
}
