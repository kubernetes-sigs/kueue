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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// VolumeBundleWrapper wraps a VolumeBundle.
type VolumeBundleWrapper struct{ v1alpha1.VolumeBundle }

// MakeVolumeBundle creates a wrapper for a VolumeBundle
func MakeVolumeBundle(name, namespace string) *VolumeBundleWrapper {
	return &VolumeBundleWrapper{
		VolumeBundle: v1alpha1.VolumeBundle{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
	}
}

// Obj returns the inner VolumeBundle.
func (vb *VolumeBundleWrapper) Obj() *v1alpha1.VolumeBundle {
	return &vb.VolumeBundle
}

// WithVolume add volume on the volumes.
func (vb *VolumeBundleWrapper) WithVolume(name, localObjectReferenceName string) *VolumeBundleWrapper {
	vb.VolumeBundle.Spec.Volumes = append(vb.VolumeBundle.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: localObjectReferenceName,
				},
			},
		},
	})
	return vb
}

// WithVolumeMount add volume mount on the volumes.
func (vb *VolumeBundleWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *VolumeBundleWrapper {
	vb.VolumeBundle.Spec.ContainerVolumeMounts = append(vb.VolumeBundle.Spec.ContainerVolumeMounts, volumeMount)
	return vb
}

// WithEnvVar add EnvVar to EnvVars.
func (vb *VolumeBundleWrapper) WithEnvVar(envVar corev1.EnvVar) *VolumeBundleWrapper {
	vb.Spec.EnvVars = append(vb.Spec.EnvVars, envVar)
	return vb
}
