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
)

const (
	defaultGCInterval       = time.Minute
	defaultOrigin           = "multikueue"
	defaultKeepReadyTimeout = 5 * time.Minute
)

type SetupOptions struct {
	gcInterval       time.Duration
	origin           string
	keepReadyTimeout time.Duration
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

// WithKeepReadyTimeout - sets the time for which the multikueue
// admission check is kept in Ready state after the connection to
// the admitting worker cluster is lost.
func WithKeepReadyTimeout(d time.Duration) SetupOption {
	return func(o *SetupOptions) {
		o.keepReadyTimeout = d
	}
}

func SetupControllers(mgr ctrl.Manager, namespace string, opts ...SetupOption) error {
	options := &SetupOptions{
		gcInterval:       defaultGCInterval,
		origin:           defaultOrigin,
		keepReadyTimeout: defaultKeepReadyTimeout,
	}

	for _, o := range opts {
		o(options)
	}

	helper, err := newMultiKueueStoreHelper(mgr.GetClient())
	if err != nil {
		return err
	}

	cRec := newClustersReconciler(mgr.GetClient(), namespace, options.gcInterval, options.origin)
	err = cRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	acRec := newACReconciler(mgr.GetClient(), helper)
	err = acRec.setupWithManager(mgr)
	if err != nil {
		return err
	}

	wlRec := newWlReconciler(mgr.GetClient(), helper, cRec, options.origin, options.keepReadyTimeout)
	return wlRec.setupWithManager(mgr)
}
