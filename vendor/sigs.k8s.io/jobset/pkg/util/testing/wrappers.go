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

package testing

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	"sigs.k8s.io/jobset/pkg/util/collections"
)

// TestPodSpec is the default pod spec used for testing.
var TestPodSpec = corev1.PodSpec{
	RestartPolicy: "Never",
	Containers: []corev1.Container{
		{
			Name:  "test-container",
			Image: "busybox:latest",
		},
	},
}

// JobSetWrapper wraps a JobSet.
type JobSetWrapper struct {
	jobset.JobSet
}

// MakeJobSet creates a wrapper for a JobSet.
func MakeJobSet(name, ns string) *JobSetWrapper {
	return &JobSetWrapper{
		jobset.JobSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: jobset.JobSetSpec{
				ReplicatedJobs: []jobset.ReplicatedJob{},
				Network:        &jobset.Network{},
			},
		},
	}
}

// Conditions sets the value of jobSet.status.conditions
func (j *JobSetWrapper) Conditions(conditions []metav1.Condition) *JobSetWrapper {
	j.Status.Conditions = conditions
	return j
}

// ManagedBy sets the value of jobSet.spec.managedBy
func (j *JobSetWrapper) ManagedBy(managedBy string) *JobSetWrapper {
	j.Spec.ManagedBy = ptr.To(managedBy)
	return j
}

// SuccessPolicy sets the value of jobSet.spec.successPolicy
func (j *JobSetWrapper) SuccessPolicy(policy *jobset.SuccessPolicy) *JobSetWrapper {
	j.Spec.SuccessPolicy = policy
	return j
}

// FailurePolicy sets the value of jobSet.spec.failurePolicy
func (j *JobSetWrapper) FailurePolicy(policy *jobset.FailurePolicy) *JobSetWrapper {
	j.Spec.FailurePolicy = policy
	return j
}

// StartupPolicy sets the value of jobSet.spec.startupPolicy
func (j *JobSetWrapper) StartupPolicy(policy *jobset.StartupPolicy) *JobSetWrapper {
	j.Spec.StartupPolicy = policy
	return j
}

// SetAnnotations sets the value of the jobSet.metadata.annotations.
func (j *JobSetWrapper) SetAnnotations(annotations map[string]string) *JobSetWrapper {
	j.Annotations = annotations
	return j
}

// SetLabels sets the value of the jobSet.metadata.labels.
func (j *JobSetWrapper) SetLabels(labels map[string]string) *JobSetWrapper {
	j.Labels = labels
	return j
}

// SetGenerateName sets the JobSet name.
func (j *JobSetWrapper) SetGenerateName(namePrefix string) *JobSetWrapper {
	// Name and GenerateName are mutually exclusive, so we must unset the Name field.
	j.Name = ""
	j.GenerateName = namePrefix
	return j
}

// Obj returns the inner JobSet.
func (j *JobSetWrapper) Obj() *jobset.JobSet {
	return &j.JobSet
}

// ReplicatedJob adds a single ReplicatedJob to the JobSet.
func (j *JobSetWrapper) ReplicatedJob(job jobset.ReplicatedJob) *JobSetWrapper {
	j.JobSet.Spec.ReplicatedJobs = append(j.JobSet.Spec.ReplicatedJobs, job)
	return j
}

// Suspend adds a suspend flag to JobSet
func (j *JobSetWrapper) Suspend(suspend bool) *JobSetWrapper {
	j.JobSet.Spec.Suspend = ptr.To(suspend)
	return j
}

// Coordinator sets the Coordinator field on the JobSet spec.
func (j *JobSetWrapper) Coordinator(coordinator *jobset.Coordinator) *JobSetWrapper {
	j.JobSet.Spec.Coordinator = coordinator
	return j
}

// NetworkSubdomain sets the value of JobSet.Network.Subdomain
func (j *JobSetWrapper) NetworkSubdomain(val string) *JobSetWrapper {
	j.JobSet.Spec.Network.Subdomain = val
	return j
}

// EnableDNSHostnames sets the value of JobSet.Network.EnableDNSHostnames.
func (j *JobSetWrapper) EnableDNSHostnames(val bool) *JobSetWrapper {
	j.JobSet.Spec.Network.EnableDNSHostnames = ptr.To(val)
	return j
}

// PublishNotReadyAddresses sets the value of JobSet.Network.PublishNotReadyAddresses.
func (j *JobSetWrapper) PublishNotReadyAddresses(val bool) *JobSetWrapper {
	j.JobSet.Spec.Network.PublishNotReadyAddresses = ptr.To(val)
	return j
}

// TTLSecondsAfterFinished sets the value of JobSet.Spec.TTLSecondsAfterFinished
func (j *JobSetWrapper) TTLSecondsAfterFinished(seconds int32) *JobSetWrapper {
	j.Spec.TTLSecondsAfterFinished = &seconds
	return j
}

// CompletedCondition adds a JobSetCompleted condition to the JobSet Status.
func (j *JobSetWrapper) CompletedCondition(completedAt metav1.Time) *JobSetWrapper {
	c := metav1.Condition{Type: string(jobset.JobSetCompleted), Status: metav1.ConditionTrue, LastTransitionTime: completedAt}
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

// FailedCondition adds a JobSetFailed condition to the JobSet Status.
func (j *JobSetWrapper) FailedCondition(failedAt metav1.Time) *JobSetWrapper {
	c := metav1.Condition{Type: string(jobset.JobSetFailed), Status: metav1.ConditionTrue, LastTransitionTime: failedAt}
	j.Status.Conditions = append(j.Status.Conditions, c)
	return j
}

// TerminalState sets the value of JobSet.Status.TerminalState.
func (j *JobSetWrapper) TerminalState(terminalState jobset.JobSetConditionType) *JobSetWrapper {
	j.Status.TerminalState = string(terminalState)
	return j
}

func (j *JobSetWrapper) DeletionTimestamp(deletionTimestamp *metav1.Time) *JobSetWrapper {
	j.ObjectMeta.DeletionTimestamp = deletionTimestamp
	return j

}

func (j *JobSetWrapper) Finalizers(finalizers []string) *JobSetWrapper {
	j.ObjectMeta.Finalizers = finalizers
	return j
}

// ReplicatedJobWrapper wraps a ReplicatedJob.
type ReplicatedJobWrapper struct {
	jobset.ReplicatedJob
}

// MakeReplicatedJob creates a wrapper for a ReplicatedJob.
func MakeReplicatedJob(name string) *ReplicatedJobWrapper {
	return &ReplicatedJobWrapper{
		jobset.ReplicatedJob{
			Name: name,
		},
	}
}

// Job sets the Job spec for the ReplicatedJob template.
func (r *ReplicatedJobWrapper) Job(jobSpec batchv1.JobTemplateSpec) *ReplicatedJobWrapper {
	r.Template = jobSpec
	return r
}

// Replicas sets the value of the ReplicatedJob.Replicas.
func (r *ReplicatedJobWrapper) Replicas(val int32) *ReplicatedJobWrapper {
	r.ReplicatedJob.Replicas = val
	return r
}

// Subdomain sets the subdomain on the PodSpec
// We artificially do this because the webhook does not work in testing
func (r *ReplicatedJobWrapper) Subdomain(subdomain string) *ReplicatedJobWrapper {
	r.Template.Spec.Template.Spec.Subdomain = subdomain
	return r
}

// Obj returns the inner ReplicatedJob.
func (r *ReplicatedJobWrapper) Obj() jobset.ReplicatedJob {
	return r.ReplicatedJob
}

// JobTemplateWrapper wraps a JobTemplateSpec.
type JobTemplateWrapper struct {
	batchv1.JobTemplateSpec
}

// MakeJobTemplate creates a wrapper for a JobTemplateSpec.
func MakeJobTemplate(name, ns string) *JobTemplateWrapper {
	return &JobTemplateWrapper{
		batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{},
				},
			},
		},
	}
}

// PodFailurePolicy sets the pod failure policy on the job template.
func (j *JobTemplateWrapper) PodFailurePolicy(policy *batchv1.PodFailurePolicy) *JobTemplateWrapper {
	j.Spec.PodFailurePolicy = policy
	return j
}

// CompletionMode sets the value of job.spec.completionMode
func (j *JobTemplateWrapper) CompletionMode(mode batchv1.CompletionMode) *JobTemplateWrapper {
	j.Spec.CompletionMode = &mode
	return j
}

// PodTemplateSpec sets the pod template spec in a Job spec.
func (j *JobTemplateWrapper) PodTemplateSpec(podTemplateSpec corev1.PodTemplateSpec) *JobTemplateWrapper {
	j.Spec.Template = podTemplateSpec
	return j
}

// PodSpec sets the pod spec in a Job template.
func (j *JobTemplateWrapper) PodSpec(podSpec corev1.PodSpec) *JobTemplateWrapper {
	j.Spec.Template.Spec = podSpec
	return j
}

// SetAnnotations sets the annotations on the Job template.
func (j *JobTemplateWrapper) SetAnnotations(annotations map[string]string) *JobTemplateWrapper {
	j.Annotations = annotations
	return j
}

// Obj returns the inner batchv1.JobTemplateSpec
func (j *JobTemplateWrapper) Obj() batchv1.JobTemplateSpec {
	return j.JobTemplateSpec
}

// JobWrapper wraps a Job.
type JobWrapper struct {
	batchv1.Job
}

// MakeJob creates a wrapper for a Job.
func MakeJob(jobName, ns string) *JobWrapper {
	return &JobWrapper{
		batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: ns,
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{},
				},
			},
		},
	}
}

// Affinity sets the pod affinities/anti-affinities for the pod template spec.
func (j *JobWrapper) Affinity(affinity *corev1.Affinity) *JobWrapper {
	j.Spec.Template.Spec.Affinity = affinity
	return j
}

// JobLabels merges the given labels to the existing Job labels.
// Duplicate keys will be overwritten by the new annotations (given in the function
// parameter).
func (j *JobWrapper) JobLabels(labels map[string]string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels = collections.MergeMaps(j.Labels, labels)
	return j
}

// JobAnnotations merges the given annotations to the existing Job annotations.
// Duplicate keys will be overwritten by the new annotations (given in the function
// parameter).
func (j *JobWrapper) JobAnnotations(annotations map[string]string) *JobWrapper {
	if j.Annotations == nil {
		j.Annotations = make(map[string]string)
	}
	j.Annotations = collections.MergeMaps(j.Annotations, annotations)
	return j
}

// PodLabels merges the given labels to the existing Pod labels.
// Duplicate keys will be overwritten by the new annotations (given in the function
// parameter).
func (j *JobWrapper) PodLabels(labels map[string]string) *JobWrapper {
	if j.Spec.Template.Labels == nil {
		j.Spec.Template.Labels = make(map[string]string)
	}
	j.Spec.Template.Labels = collections.MergeMaps(j.Spec.Template.Labels, labels)
	return j
}

// Suspend sets suspend in the job spec.
func (j *JobWrapper) Suspend(suspend bool) *JobWrapper {
	j.Spec.Suspend = ptr.To(suspend)
	return j
}

// PodAnnotations merges the given annotations to the existing Pod annotations.
// Duplicate keys will be overwritten by the new annotations (given in the function
// parameter).
func (j *JobWrapper) PodAnnotations(annotations map[string]string) *JobWrapper {
	if j.Spec.Template.Annotations == nil {
		j.Spec.Template.Annotations = make(map[string]string)
	}
	j.Spec.Template.Annotations = collections.MergeMaps(j.Spec.Template.Annotations, annotations)
	return j
}

// PodSpec sets the pod template spec.
func (j *JobWrapper) PodSpec(podSpec corev1.PodSpec) *JobWrapper {
	j.Spec.Template.Spec = podSpec
	return j
}

// Subdomain sets the pod template spec subdomain.
func (j *JobWrapper) Subdomain(subdomain string) *JobWrapper {
	j.Spec.Template.Spec.Subdomain = subdomain
	return j
}

// Parallelism sets the job spec parallelism.
func (j *JobWrapper) Parallelism(parallelism int32) *JobWrapper {
	j.Spec.Parallelism = ptr.To(parallelism)
	return j
}

// Completions sets the job spec completions.
func (j *JobWrapper) Completions(completions int32) *JobWrapper {
	j.Spec.Completions = ptr.To(completions)
	return j
}

// Succeeded sets the job status succeeded.
func (j *JobWrapper) Succeeded(succeeded int32) *JobWrapper {
	j.Status.Succeeded = succeeded
	return j
}

// Active sets the job status active.
func (j *JobWrapper) Active(active int32) *JobWrapper {
	j.Status.Active = active
	return j
}

// Ready sets the job status ready.
func (j *JobWrapper) Ready(ready int32) *JobWrapper {
	j.Status.Ready = ptr.To(ready)
	return j
}

// Tolerations set the tolerations.
func (j *JobWrapper) Tolerations(t []corev1.Toleration) *JobWrapper {
	j.Spec.Template.Spec.Tolerations = t
	return j
}

// NodeSelector sets the node selector.
func (j *JobWrapper) NodeSelector(nodeSelector map[string]string) *JobWrapper {
	j.Spec.Template.Spec.NodeSelector = nodeSelector
	return j
}

// Obj returns the wrapped Job.
func (j *JobWrapper) Obj() *batchv1.Job {
	return &j.Job
}

// PodWrapper wraps a Pod.
type PodWrapper struct {
	corev1.Pod
}

// MakePod creates a wrapper for a Pod.
func MakePod(podName, ns string) *PodWrapper {
	return &PodWrapper{
		corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: ns,
			},
			Spec: corev1.PodSpec{},
		},
	}
}

// AddAnnotation add a pod annotation.
func (p *PodWrapper) AddAnnotation(key, value string) *PodWrapper {
	p.ObjectMeta.Annotations[key] = value
	return p
}

// AddLabel add a pod label.
func (p *PodWrapper) AddLabel(key, value string) *PodWrapper {
	p.ObjectMeta.Labels[key] = value
	return p
}

// Annotations sets the pod annotations.
func (p *PodWrapper) Annotations(annotations map[string]string) *PodWrapper {
	p.ObjectMeta.Annotations = annotations
	return p
}

// Labels sets the pod labels.
func (p *PodWrapper) Labels(labels map[string]string) *PodWrapper {
	p.ObjectMeta.Labels = labels
	return p
}

// SetConditions sets the value of the pod.status.conditions.
func (p *PodWrapper) SetConditions(conditions []corev1.PodCondition) *PodWrapper {
	p.Status.Conditions = conditions
	return p
}

// NodeSelector sets the value of the pod.spec.nodeSelector.
func (p *PodWrapper) NodeSelector(nodeSelector map[string]string) *PodWrapper {
	p.Spec.NodeSelector = nodeSelector
	return p
}

// Obj returns the wrapped Pod.
func (p *PodWrapper) Obj() corev1.Pod {
	return p.Pod
}
