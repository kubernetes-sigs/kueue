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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

// PodWrapper wraps a Pod.
type PodWrapper struct{ corev1.Pod }

// MakePod creates a wrapper for a Pod
func MakePod(name, ns string) *PodWrapper {
	return &PodWrapper{
		corev1.Pod{
			TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		},
	}
}

// Obj returns the inner Pod.
func (j *PodWrapper) Obj() *corev1.Pod {
	return &j.Pod
}

// GenerateName updates generateName.
func (j *PodWrapper) GenerateName(v string) *PodWrapper {
	j.ObjectMeta.GenerateName = v
	return j
}

// Profile sets the profile label.
func (j *PodWrapper) Profile(v string) *PodWrapper {
	return j.Label(constants.ProfileLabel, v)
}

// LocalQueue sets the localqueue label.
func (j *PodWrapper) LocalQueue(v string) *PodWrapper {
	return j.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (j *PodWrapper) Label(key, value string) *PodWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.ObjectMeta.Labels[key] = value
	return j
}

// WithContainer add container on the pod template.
func (j *PodWrapper) WithContainer(container corev1.Container) *PodWrapper {
	j.Pod.Spec.Containers = append(j.Pod.Spec.Containers, container)
	return j
}

// RestartPolicy updates the restartPolicy on the pod template.
func (j *PodWrapper) RestartPolicy(restartPolicy corev1.RestartPolicy) *PodWrapper {
	j.Pod.Spec.RestartPolicy = restartPolicy
	return j
}

// CreationTimestamp sets the .metadata.creationTimestamp.
func (j *PodWrapper) CreationTimestamp(t time.Time) *PodWrapper {
	j.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return j
}

// StartTime sets the .status.startTime.
func (j *PodWrapper) StartTime(t time.Time) *PodWrapper {
	j.Status.StartTime = &metav1.Time{Time: t}
	return j
}

// Spec set pod spec.
func (j *PodWrapper) Spec(spec corev1.PodSpec) *PodWrapper {
	j.Pod.Spec = spec
	return j
}
