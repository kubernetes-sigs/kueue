/*
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
*/

package multikueue

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

const (
	defaultGCInterval        = time.Minute
	defaultOrigin            = "multikueue"
	defaultWorkerLostTimeout = 5 * time.Minute
)

type SetupOptions struct {
	controllerName    string
	gcInterval        time.Duration
	origin            string
	workerLostTimeout time.Duration
	eventsBatchPeriod time.Duration
	adapters          map[string]jobframework.MultiKueueAdapter
}

type SetupOption func(o *SetupOptions)

// WithGCInterval - sets the interval between two garbage collection runs.
// If 0 the garbage collection is disabled.
func WithGCInterval(i time.Duration) SetupOption {
	return func(o *SetupOptions) {
		o.gcInterval = i
	}
}

// WithOrigin - sets the multikueue-origin label value used by this manager
func WithOrigin(origin string) SetupOption {
	return func(o *SetupOptions) {
		o.origin = origin
	}
}

// WithWorkerLostTimeout - sets the time for which the multikueue
// admission check is kept in Ready state after the connection to
// the admitting worker cluster is lost.
func WithWorkerLostTimeout(d time.Duration) SetupOption {
	return func(o *SetupOptions) {
		o.workerLostTimeout = d
	}
}

// WithEventsBatchPeriod - sets the delay used when adding remote triggered
// events to the workload's reconcile queue.
func WithEventsBatchPeriod(d time.Duration) SetupOption {
	return func(o *SetupOptions) {
		o.eventsBatchPeriod = d
	}
}

// WithControllerName - sets the controller name for which the multikueue
// admission check match.
func WithControllerName(controllerName string) SetupOption {
	return func(o *SetupOptions) {
		o.controllerName = controllerName
	}
}

// WithAdapters - sets all the MultiKueue adaptors.
func WithAdapters(adapters map[string]jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters = adapters
	}
}

// WithAdapters - sets or updates the adadpter of the MultiKueue adaptors.
func WithAdapter(adapter jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters[adapter.GVK().String()] = adapter
	}
}

func NewSetupOptions() *SetupOptions {
	return &SetupOptions{
		gcInterval:        defaultGCInterval,
		origin:            defaultOrigin,
		workerLostTimeout: defaultWorkerLostTimeout,
		eventsBatchPeriod: constants.UpdatesBatchPeriod,
		controllerName:    kueuealpha.MultiKueueControllerName,
		adapters:          make(map[string]jobframework.MultiKueueAdapter),
	}
}

func SetupControllers(mgr ctrl.Manager, namespace string, opts ...SetupOption) error {
	options := NewSetupOptions()

	for _, o := range opts {
		o(options)
	}

	helper, err := newMultiKueueStoreHelper(mgr.GetClient())
	if err != nil {
		return err
	}

	fsWatcher := newKubeConfigFSWatcher()
	err = mgr.Add(fsWatcher)
	if err != nil {
		return err
	}

	cRec := newClustersReconciler(mgr.GetClient(), namespace, *options, fsWatcher)
	err = cRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	acRec := newACReconciler(mgr.GetClient(), helper, *options)
	err = acRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	wlRec := newWlReconciler(mgr.GetClient(), helper, cRec, *options)
	return wlRec.setupWithManager(mgr)
}
