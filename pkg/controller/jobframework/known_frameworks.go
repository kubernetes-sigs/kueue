/*
Copyright 2023 The Kubernetes Authors.

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

package jobframework

import (
	"strings"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func KnownWorkloadOwner(owner *metav1.OwnerReference) bool {
	return IsMPIJob(owner)
}

func KnownWorkloadOwnerObject(owner *metav1.OwnerReference) client.Object {
	if IsMPIJob(owner) {
		return &kubeflow.MPIJob{}
	}
	return nil
}

func IsMPIJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "MPIJob" && strings.HasPrefix(owner.APIVersion, "kubeflow.org/v2")
}
