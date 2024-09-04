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

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutil "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

// RayJobWrapper wraps a RayJob.
type RayJobWrapper struct{ rayv1.RayJob }

// MakeRayJob creates a wrapper for a RayJob
func MakeRayJob(name, ns string) *RayJobWrapper {
	return &RayJobWrapper{
		rayv1.RayJob{
			TypeMeta: metav1.TypeMeta{Kind: "RayJob", APIVersion: "ray.io/v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RayJob.
func (j *RayJobWrapper) Obj() *rayv1.RayJob {
	return &j.RayJob
}

// GenerateName updates generateName.
func (j *RayJobWrapper) GenerateName(v string) *RayJobWrapper {
	j.ObjectMeta.GenerateName = v
	return j
}

// CreationTimestamp sets the .metadata.creationTimestamp
func (j *RayJobWrapper) CreationTimestamp(t time.Time) *RayJobWrapper {
	j.RayJob.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return j
}

// Profile sets the profile label.
func (j *RayJobWrapper) Profile(v string) *RayJobWrapper {
	return j.Label(constants.ProfileLabel, v)
}

// Mode sets the profile label.
func (j *RayJobWrapper) Mode(v v1alpha1.ApplicationProfileMode) *RayJobWrapper {
	return j.Label(constants.ModeLabel, string(v))
}

// LocalQueue sets the localqueue label.
func (j *RayJobWrapper) LocalQueue(v string) *RayJobWrapper {
	return j.Label(kueueconstants.QueueLabel, v)
}

// Label sets the label key and value.
func (j *RayJobWrapper) Label(key, value string) *RayJobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.ObjectMeta.Labels[key] = value
	return j
}

// Annotation sets the label key and value.
func (j *RayJobWrapper) Annotation(key, value string) *RayJobWrapper {
	if j.Annotations == nil {
		j.Annotations = make(map[string]string)
	}
	j.ObjectMeta.Annotations[key] = value
	return j
}

// WithWorkerGroupSpec add worker group to the ray cluster template.
func (j *RayJobWrapper) WithWorkerGroupSpec(spec rayv1.WorkerGroupSpec) *RayJobWrapper {
	if j.RayJob.Spec.RayClusterSpec == nil {
		j.RayJob.Spec.RayClusterSpec = &rayv1.RayClusterSpec{}
	}

	j.RayJob.Spec.RayClusterSpec.WorkerGroupSpecs = append(j.RayJob.Spec.RayClusterSpec.WorkerGroupSpecs, spec)

	return j
}

// Spec set job spec.
func (j *RayJobWrapper) Spec(spec rayv1.RayJobSpec) *RayJobWrapper {
	j.RayJob.Spec = spec
	return j
}

// Entrypoint set entrypoint.
func (j *RayJobWrapper) Entrypoint(entrypoint string) *RayJobWrapper {
	j.RayJob.Spec.Entrypoint = entrypoint
	return j
}

// JobStatus set jobStatus.
func (j *RayJobWrapper) JobStatus(jobStatus rayv1.JobStatus) *RayJobWrapper {
	j.RayJob.Status.JobStatus = jobStatus
	return j
}

// JobDeploymentStatus set jobDeploymentStatus.
func (j *RayJobWrapper) JobDeploymentStatus(jobDeploymentStatus rayv1.JobDeploymentStatus) *RayJobWrapper {
	j.RayJob.Status.JobDeploymentStatus = jobDeploymentStatus
	return j
}

// Reason set reason.
func (j *RayJobWrapper) Reason(reason rayv1.JobFailedReason) *RayJobWrapper {
	j.RayJob.Status.Reason = reason
	return j
}

// Message set message.
func (j *RayJobWrapper) Message(message string) *RayJobWrapper {
	j.RayJob.Status.Message = message
	return j
}

// StartTime set startTime.
func (j *RayJobWrapper) StartTime(startTime time.Time) *RayJobWrapper {
	j.RayJob.Status.StartTime = ptr.To(metav1.NewTime(startTime))
	return j
}

// EndTime set endTime.
func (j *RayJobWrapper) EndTime(endTime time.Time) *RayJobWrapper {
	j.RayJob.Status.EndTime = ptr.To(metav1.NewTime(endTime))
	return j
}

// Suspend set suspend.
func (j *RayJobWrapper) Suspend(suspend bool) *RayJobWrapper {
	j.RayJob.Spec.Suspend = suspend
	return j
}

// WithRayClusterLabelSelector sets the ClusterSelector.
func (j *RayJobWrapper) WithRayClusterLabelSelector(v string) *RayJobWrapper {
	if j.RayJob.Spec.ClusterSelector == nil {
		j.RayJob.Spec.ClusterSelector = map[string]string{}
	}
	j.RayJob.Spec.ClusterSelector[rayutil.RayClusterLabelKey] = v
	return j
}

// RayClusterName set rayClusterName.
func (j *RayJobWrapper) RayClusterName(rayClusterName string) *RayJobWrapper {
	j.RayJob.Status.RayClusterName = rayClusterName
	return j
}
