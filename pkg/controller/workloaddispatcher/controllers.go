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
)

func SetupControllers(mgr ctrl.Manager, cfg *configapi.Configuration, dispatcherName string) (string, error) {
	helper, err := admissioncheck.NewMultiKueueStoreHelper(mgr.GetClient())
	if err != nil {
		return "", err
	}

	idRec := NewIncrementalDispatcherReconciler(mgr.GetClient(), helper, dispatcherName)
	err = idRec.SetupWithManager(mgr, cfg)
	if err != nil {
		return "multikueue-incremental-dispatcher", err
	}

	return "", nil
}
