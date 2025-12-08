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

package dispatcher

import (
	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// SetupOption configures the dispatcher controllers setup.
type SetupOption func(*setupOptions)

type setupOptions struct {
	roleTracker *roletracker.RoleTracker
}

// WithRoleTracker sets the roleTracker for dispatcher controllers.
func WithRoleTracker(tracker *roletracker.RoleTracker) SetupOption {
	return func(o *setupOptions) {
		o.roleTracker = tracker
	}
}

func SetupControllers(mgr ctrl.Manager, cfg *configapi.Configuration, opts ...SetupOption) (string, error) {
	options := &setupOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if *cfg.MultiKueue.DispatcherName != configapi.MultiKueueDispatcherModeIncremental {
		return "", nil
	}

	helper, err := admissioncheck.NewMultiKueueStoreHelper(mgr.GetClient())
	if err != nil {
		return "", err
	}

	idRec := NewIncrementalDispatcherReconciler(mgr.GetClient(), helper, options.roleTracker)
	err = idRec.SetupWithManager(mgr, cfg)
	if err != nil {
		return "multikueue-incremental-dispatcher", err
	}

	return "", nil
}
