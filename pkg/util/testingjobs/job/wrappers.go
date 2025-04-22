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

package testing

import (
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/testing"
)

// JobWrapper wraps a Job.
type JobWrapper struct{ batchv1.Job }

// MakeJob creates a wrapper for a suspended job with a single container and parallelism=1.
func MakeJob(name, ns string) *JobWrapper {
	return &JobWrapper{batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To[int32](1),
			Suspend:     ptr.To(true),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:      "c",
							Image:     "pause",
							Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}},
						},
					},
					NodeSelector: map[string]string{},
				},
			},
		},
	}}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *batchv1.Job {
	return &j.Job
}

// Clone returns deep copy of the Job.
func (j *JobWrapper) Clone() *JobWrapper {
	return &JobWrapper{Job: *j.DeepCopy()}
}

func (j *JobWrapper) BackoffLimit(limit int32) *JobWrapper {
	j.Spec.BackoffLimit = ptr.To(limit)
	return j
}

func (j *JobWrapper) BackoffLimitPerIndex(limit int32) *JobWrapper {
	j.Spec.BackoffLimitPerIndex = ptr.To(limit)
	return j
}

func (j *JobWrapper) CompletionMode(mode batchv1.CompletionMode) *JobWrapper {
	j.Spec.CompletionMode = &mode
	return j
}

func (j *JobWrapper) TerminationGracePeriod(seconds int64) *JobWrapper {
	j.Spec.Template.Spec.TerminationGracePeriodSeconds = ptr.To(seconds)
	return j
}

// Suspend updates the suspend status of the job
func (j *JobWrapper) Suspend(s bool) *JobWrapper {
	j.Spec.Suspend = ptr.To(s)
	return j
}

// Parallelism updates job parallelism.
func (j *JobWrapper) Parallelism(p int32) *JobWrapper {
	j.Spec.Parallelism = ptr.To(p)
	return j
}

// Completions updates job completions.
func (j *JobWrapper) Completions(p int32) *JobWrapper {
	j.Spec.Completions = ptr.To(p)
	return j
}

// Indexed sets the job's completion to Indexed of NonIndexed
func (j *JobWrapper) Indexed(indexed bool) *JobWrapper {
	mode := batchv1.NonIndexedCompletion
	if indexed {
		mode = batchv1.IndexedCompletion
	}
	j.Spec.CompletionMode = &mode
	return j
}

// PriorityClass updates job priorityclass.
func (j *JobWrapper) PriorityClass(pc string) *JobWrapper {
	j.Spec.Template.Spec.PriorityClassName = pc
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *JobWrapper) WorkloadPriorityClass(wpc string) *JobWrapper {
	return j.Label(constants.WorkloadPriorityClassLabel, wpc)
}

// Queue updates the queue name of the job
func (j *JobWrapper) Queue(queue string) *JobWrapper {
	return j.Label(constants.QueueLabel, queue)
}

// Label sets the label key and value
func (j *JobWrapper) Label(key, value string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// QueueNameAnnotation updates the queue name of the job by annotation (deprecated)
func (j *JobWrapper) QueueNameAnnotation(queue string) *JobWrapper {
	return j.SetAnnotation(constants.QueueAnnotation, queue)
}

func (j *JobWrapper) SetAnnotation(key, content string) *JobWrapper {
	j.Annotations[key] = content
	return j
}

// Toleration adds a toleration to the job.
func (j *JobWrapper) Toleration(t corev1.Toleration) *JobWrapper {
	j.Spec.Template.Spec.Tolerations = append(j.Spec.Template.Spec.Tolerations, t)
	return j
}

// NodeSelector adds a node selector to the job.
func (j *JobWrapper) NodeSelector(k, v string) *JobWrapper {
	j.Spec.Template.Spec.NodeSelector[k] = v
	return j
}

// PodAnnotation sets annotation at the pod template level
func (j *JobWrapper) PodAnnotation(k, v string) *JobWrapper {
	if j.Spec.Template.Annotations == nil {
		j.Spec.Template.Annotations = make(map[string]string)
	}
	j.Spec.Template.Annotations[k] = v
	return j
}

// PodLabel sets label at the pod template level
func (j *JobWrapper) PodLabel(k, v string) *JobWrapper {
	if j.Spec.Template.Labels == nil {
		j.Spec.Template.Labels = make(map[string]string)
	}
	j.Spec.Template.Labels[k] = v
	return j
}

// Request adds a resource request to the default container.
func (j *JobWrapper) Request(r corev1.ResourceName, v string) *JobWrapper {
	j.Spec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return j
}

// Limit adds a resource limit to the default container.
func (j *JobWrapper) Limit(r corev1.ResourceName, v string) *JobWrapper {
	j.Spec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	return j
}

// RequestAndLimit adds a resource request and limit to the default container.
func (j *JobWrapper) RequestAndLimit(r corev1.ResourceName, v string) *JobWrapper {
	return j.Request(r, v).Limit(r, v)
}

func (j *JobWrapper) Image(image string, args []string) *JobWrapper {
	j.Spec.Template.Spec.Containers[0].Image = image
	j.Spec.Template.Spec.Containers[0].Args = args
	return j
}

// OwnerReference adds a ownerReference to the default container.
func (j *JobWrapper) OwnerReference(ownerName string, ownerGVK schema.GroupVersionKind) *JobWrapper {
	testing.AppendOwnerReference(&j.Job, ownerGVK, ownerName, ownerName, ptr.To(true), ptr.To(true))
	return j
}

func (j *JobWrapper) Containers(containers ...corev1.Container) *JobWrapper {
	j.Spec.Template.Spec.Containers = containers
	return j
}

// UID updates the uid of the job.
func (j *JobWrapper) UID(uid string) *JobWrapper {
	j.ObjectMeta.UID = types.UID(uid)
	return j
}

// StartTime sets the .status.startTime
func (j *JobWrapper) StartTime(t time.Time) *JobWrapper {
	j.Status.StartTime = &metav1.Time{Time: t}
	return j
}

// Active sets the .status.active
func (j *JobWrapper) Active(c int32) *JobWrapper {
	j.Status.Active = c
	return j
}

// Failed sets the .status.failed
func (j *JobWrapper) Failed(c int32) *JobWrapper {
	j.Status.Failed = c
	return j
}

// Ready sets the .status.ready
func (j *JobWrapper) Ready(c int32) *JobWrapper {
	j.Status.Ready = &c
	return j
}

// Condition adds a condition
func (j *JobWrapper) Condition(c batchv1.JobCondition) *JobWrapper {
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

// Generation sets the generation
func (j *JobWrapper) Generation(g int64) *JobWrapper {
	j.ObjectMeta.Generation = g
	return j
}

func SetContainerDefaults(c *corev1.Container) {
	if c.TerminationMessagePath == "" {
		c.TerminationMessagePath = "/dev/termination-log"
	}

	if c.TerminationMessagePolicy == "" {
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	}

	if c.ImagePullPolicy == "" {
		c.ImagePullPolicy = corev1.PullIfNotPresent
	}
}

// ManagedBy adds a managedby.
func (j *JobWrapper) ManagedBy(c string) *JobWrapper {
	j.Spec.ManagedBy = &c
	return j
}

func (j *JobWrapper) SetTypeMeta() *JobWrapper {
	j.APIVersion = batchv1.SchemeGroupVersion.String()
	j.Kind = "Job"
	return j
}
