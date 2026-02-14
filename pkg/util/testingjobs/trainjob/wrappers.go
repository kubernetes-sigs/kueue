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

package trainjob

import (
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

type TrainJobWrapper struct{ kftrainerapi.TrainJob }

// MakeTrainJob creates a wrapper for a suspended TrainJob
func MakeTrainJob(name, ns string) *TrainJobWrapper {
	return &TrainJobWrapper{kftrainerapi.TrainJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: kftrainerapi.TrainJobSpec{
			Suspend: ptr.To(true),
			Trainer: &kftrainerapi.Trainer{
				ResourcesPerNode: &corev1.ResourceRequirements{
					Requests: map[corev1.ResourceName]resource.Quantity{},
					Limits:   map[corev1.ResourceName]resource.Quantity{},
				},
			},
		},
	}}
}

// Clone returns deep copy of the TrainJobWrapper
func (t *TrainJobWrapper) Clone() *TrainJobWrapper {
	return &TrainJobWrapper{TrainJob: *t.DeepCopy()}
}

// PodTemplateOverrides sets the custom pod template overrides to be set to the TrainJob jobset jobs
func (t *TrainJobWrapper) PodTemplateOverrides(overrides []kftrainerapi.PodTemplateOverride) *TrainJobWrapper {
	t.Spec.PodTemplateOverrides = overrides
	return t
}

// RuntimeRefName sets the TrainingRuntime reference
func (t *TrainJobWrapper) RuntimeRef(runtimeRef kftrainerapi.RuntimeRef) *TrainJobWrapper {
	t.Spec.RuntimeRef = runtimeRef
	return t
}

// RuntimeRefName sets the TrainingRuntime reference name
func (t *TrainJobWrapper) RuntimeRefName(name string) *TrainJobWrapper {
	t.Spec.RuntimeRef.Name = name
	return t
}

// TrainerImage sets the Trainer node image
func (t *TrainJobWrapper) TrainerImage(image string, cmd, args []string) *TrainJobWrapper {
	t.Spec.Trainer.Image = &image
	t.Spec.Trainer.Command = cmd
	t.Spec.Trainer.Args = args
	return t
}

// TrainerNumNodes sets a the number of nodes that will be used in the Trainer job
func (t *TrainJobWrapper) TrainerNumNodes(numNodes int32) *TrainJobWrapper {
	t.Spec.Trainer.NumNodes = ptr.To(numNodes)
	return t
}

// Label sets a Trainjob annotation key and value
func (t *TrainJobWrapper) Annotation(key, value string) *TrainJobWrapper {
	if t.Annotations == nil {
		t.Annotations = make(map[string]string)
	}
	t.Annotations[key] = value
	return t
}

// Label sets a Trainjob label key and value
func (t *TrainJobWrapper) Label(key, value string) *TrainJobWrapper {
	if t.Labels == nil {
		t.Labels = make(map[string]string)
	}
	t.Labels[key] = value
	return t
}

// JobSetLabel sets the Trainjob jobset label key and value
func (t *TrainJobWrapper) JobSetLabel(key, value string) *TrainJobWrapper {
	if t.Spec.Labels == nil {
		t.Spec.Labels = make(map[string]string)
	}
	t.Spec.Labels[key] = value
	return t
}

// Obj returns the inner TrainJob.
func (t *TrainJobWrapper) Obj() *kftrainerapi.TrainJob {
	return &t.TrainJob
}

// Queue updates the queue name of the TrainJob.
func (t *TrainJobWrapper) Queue(queue string) *TrainJobWrapper {
	if t.Labels == nil {
		t.Labels = make(map[string]string)
	}
	t.Labels[constants.QueueLabel] = queue
	return t
}

// ManagedBy sets the managedby field of the TrainJob.
func (t *TrainJobWrapper) ManagedBy(managedBy string) *TrainJobWrapper {
	t.Spec.ManagedBy = ptr.To(managedBy)
	return t
}

// Suspend updates the suspend status of the TrainJob.
func (t *TrainJobWrapper) Suspend(s bool) *TrainJobWrapper {
	t.Spec.Suspend = ptr.To(s)
	return t
}

// Request adds a resource request to the Trainer node
func (t *TrainJobWrapper) TrainerRequest(r corev1.ResourceName, v string) *TrainJobWrapper {
	t.Spec.Trainer.ResourcesPerNode.Requests[r] = resource.MustParse(v)
	return t
}

// JobsStatus sets the job statuses of the TrainJob.
func (t *TrainJobWrapper) JobsStatus(statuses ...kftrainerapi.JobStatus) *TrainJobWrapper {
	t.Status.JobsStatus = statuses
	return t
}

// MakeClusterTrainingRuntime creates a ClusterTrainingRuntime with the jobsetSpec provided
func MakeClusterTrainingRuntime(name string, jobsetSpec jobsetapi.JobSetSpec) *kftrainerapi.ClusterTrainingRuntime {
	return &kftrainerapi.ClusterTrainingRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kftrainerapi.TrainingRuntimeSpec{
			Template: kftrainerapi.JobSetTemplateSpec{
				Spec: jobsetSpec,
			},
		},
	}
}

// MakeTrainingRuntime creates a TrainingRuntime with the jobsetSpec provided
func MakeTrainingRuntime(name, ns string, jobsetSpec jobsetapi.JobSetSpec) *kftrainerapi.TrainingRuntime {
	return &kftrainerapi.TrainingRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: kftrainerapi.TrainingRuntimeSpec{
			Template: kftrainerapi.JobSetTemplateSpec{
				Spec: jobsetSpec,
			},
		},
	}
}

type JobStatusWrapper struct{ kftrainerapi.JobStatus }

func MakeJobStatusWrapper(name string) *JobStatusWrapper {
	return &JobStatusWrapper{kftrainerapi.JobStatus{
		Name:      name,
		Ready:     ptr.To(int32(0)),
		Succeeded: ptr.To(int32(0)),
		Failed:    ptr.To(int32(0)),
		Active:    ptr.To(int32(0)),
		Suspended: ptr.To(int32(0)),
	}}
}

func (s *JobStatusWrapper) Active(v int32) *JobStatusWrapper {
	s.JobStatus.Active = ptr.To(v)
	return s
}

func (s *JobStatusWrapper) Ready(v int32) *JobStatusWrapper {
	s.JobStatus.Ready = ptr.To(v)
	return s
}

func (s *JobStatusWrapper) Succeeded(v int32) *JobStatusWrapper {
	s.JobStatus.Succeeded = ptr.To(v)
	return s
}

func (s *JobStatusWrapper) Obj() kftrainerapi.JobStatus {
	return s.JobStatus
}
