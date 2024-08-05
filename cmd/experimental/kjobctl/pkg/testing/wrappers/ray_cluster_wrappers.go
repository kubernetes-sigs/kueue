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

package wrappers

import (
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

// RayClusterWrapper wraps a RayCluster.
type RayClusterWrapper struct{ rayv1.RayCluster }

// MakeRayCluster creates a wrapper for a RayCluster
func MakeRayCluster(name, ns string) *RayClusterWrapper {
	return &RayClusterWrapper{
		rayv1.RayCluster{
			TypeMeta: metav1.TypeMeta{Kind: "RayCluster", APIVersion: "ray.io/v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RayCluster.
func (j *RayClusterWrapper) Obj() *rayv1.RayCluster {
	return &j.RayCluster
}

// GenerateName updates generateName.
func (j *RayClusterWrapper) GenerateName(v string) *RayClusterWrapper {
	j.ObjectMeta.GenerateName = v
	return j
}

// Profile sets the profile label.
func (j *RayClusterWrapper) Profile(v string) *RayClusterWrapper {
	return j.Label(constants.ProfileLabel, v)
}

// LocalQueue sets the localqueue label.
func (j *RayClusterWrapper) LocalQueue(v string) *RayClusterWrapper {
	return j.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (j *RayClusterWrapper) Label(key, value string) *RayClusterWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.ObjectMeta.Labels[key] = value
	return j
}

// WithWorkerGroupSpec add worker group to the ray cluster template.
func (j *RayClusterWrapper) WithWorkerGroupSpec(spec rayv1.WorkerGroupSpec) *RayClusterWrapper {
	j.RayCluster.Spec.WorkerGroupSpecs = append(j.RayCluster.Spec.WorkerGroupSpecs, spec)
	return j
}

// Spec set job spec.
func (j *RayClusterWrapper) Spec(spec rayv1.RayClusterSpec) *RayClusterWrapper {
	j.RayCluster.Spec = spec
	return j
}
