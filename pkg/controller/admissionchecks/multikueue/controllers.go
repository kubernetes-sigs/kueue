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

package multikueue

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

const (
	defaultGCInterval        = time.Minute
	defaultOrigin            = "multikueue"
	defaultWorkerLostTimeout = 5 * time.Minute
)

type SetupOptions struct {
	gcInterval        time.Duration
	origin            string
	workerLostTimeout time.Duration
	eventsBatchPeriod time.Duration
	adapters          map[string]jobframework.MultiKueueAdapter
	dispatcherName    string
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

// WithAdapters sets or updates the adapters of the MultiKueue adapters.
func WithAdapters(adapters map[string]jobframework.MultiKueueAdapter) SetupOption {
	return func(o *SetupOptions) {
		o.adapters = adapters
	}
}

// WithDispatcherName sets or updates the dispatcher of the MultiKueue workload.
func WithDispatcherName(dispatcherName string) SetupOption {
	return func(o *SetupOptions) {
		o.dispatcherName = dispatcherName
	}
}

func SetupControllers(mgr ctrl.Manager, namespace string, opts ...SetupOption) error {
	options := &SetupOptions{
		gcInterval:        defaultGCInterval,
		origin:            defaultOrigin,
		workerLostTimeout: defaultWorkerLostTimeout,
		eventsBatchPeriod: constants.UpdatesBatchPeriod,
		adapters:          make(map[string]jobframework.MultiKueueAdapter),
		dispatcherName:    configapi.MultiKueueDispatcherModeAllAtOnce,
	}

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

	cRec := newClustersReconciler(mgr.GetClient(), namespace, options.gcInterval, options.origin, fsWatcher, options.adapters)
	err = cRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	acRec := newACReconciler(mgr.GetClient(), helper)
	err = acRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	wlRec := newWlReconciler(mgr.GetClient(), helper, cRec, options.origin, mgr.GetEventRecorderFor(constants.WorkloadControllerName),
		options.workerLostTimeout, options.eventsBatchPeriod, options.adapters, options.dispatcherName)
	return wlRec.setupWithManager(mgr)
}
