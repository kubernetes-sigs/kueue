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

package failurerecovery

import (
	"sigs.k8s.io/controller-runtime/pkg/manager"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
)

func SetupControllers(mgr manager.Manager, cfg *configapi.Configuration) (string, error) {
	tpRec := NewTerminatingPodReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.PodTerminationControllerName),
	)
	return tpRec.SetupWithManager(mgr, cfg)
}
