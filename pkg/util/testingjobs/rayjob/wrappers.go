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

package rayjob

import (
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// JobWrapper wraps a RayJob.
type JobWrapper struct{ rayv1.RayJob }

// MakeJob creates a wrapper for a suspended rayJob
func MakeJob(name, ns string) *JobWrapper {
	return &JobWrapper{rayv1.RayJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: rayv1.RayJobSpec{
			ShutdownAfterJobFinishes: true,
			RayClusterSpec: &rayv1.RayClusterSpec{
				RayVersion: "2.9.0",
				HeadGroupSpec: rayv1.HeadGroupSpec{
					RayStartParams: map[string]string{},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: "Never",
							Containers: []corev1.Container{
								{
									Name:    "head-container",
									Command: []string{},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{},
										Limits:   corev1.ResourceList{},
									},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName:      "workers-group-0",
						Replicas:       ptr.To[int32](1),
						MinReplicas:    ptr.To[int32](0),
						MaxReplicas:    ptr.To[int32](10),
						RayStartParams: map[string]string{},
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: "Never",
								Containers: []corev1.Container{
									{
										Name:    "worker-container",
										Command: []string{},
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{},
											Limits:   corev1.ResourceList{},
										},
									},
								},
								NodeSelector: map[string]string{},
							},
						},
					},
				},
			},
			Suspend: true,
		},
	}}
}

// Obj returns the inner Job.
func (j *JobWrapper) Obj() *rayv1.RayJob {
	return &j.RayJob
}

// Suspend updates the suspend status of the job
func (j *JobWrapper) Suspend(s bool) *JobWrapper {
	j.Spec.Suspend = s
	return j
}

// Queue updates the queue name of the job
func (j *JobWrapper) Queue(queue string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

func (j *JobWrapper) RequestWorkerGroup(name corev1.ResourceName, quantity string) *JobWrapper {
	c := &j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

func (j *JobWrapper) RequestHead(name corev1.ResourceName, quantity string) *JobWrapper {
	c := &j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{name: resource.MustParse(quantity)}
	} else {
		c.Resources.Requests[name] = resource.MustParse(quantity)
	}
	return j
}

func (j *JobWrapper) ShutdownAfterJobFinishes(value bool) *JobWrapper {
	j.Spec.ShutdownAfterJobFinishes = value
	return j
}

func (j *JobWrapper) ClusterSelector(value map[string]string) *JobWrapper {
	j.Spec.ClusterSelector = value
	return j
}

func (j *JobWrapper) WithEnableAutoscaling(value *bool) *JobWrapper {
	j.Spec.RayClusterSpec.EnableInTreeAutoscaling = value
	return j
}

func (j *JobWrapper) WithWorkerGroups(workers ...rayv1.WorkerGroupSpec) *JobWrapper {
	j.Spec.RayClusterSpec.WorkerGroupSpecs = workers
	return j
}

func (j *JobWrapper) WithSubmitterPodTemplate(template corev1.PodTemplateSpec) *JobWrapper {
	j.Spec.SubmitterPodTemplate = &template
	return j
}

func (j *JobWrapper) WithSubmissionMode(submissionMode rayv1.JobSubmissionMode) *JobWrapper {
	j.Spec.SubmissionMode = submissionMode
	return j
}

func (j *JobWrapper) WithHeadGroupSpec(value rayv1.HeadGroupSpec) *JobWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec = value
	return j
}

func (j *JobWrapper) WithPriorityClassName(value string) *JobWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.PriorityClassName = value
	return j
}

func (j *JobWrapper) WithWorkerPriorityClassName(value string) *JobWrapper {
	j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.PriorityClassName = value
	return j
}

// WorkloadPriorityClass updates job workloadpriorityclass.
func (j *JobWrapper) WorkloadPriorityClass(wpc string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// Generation sets the generation of the job.
func (j *JobWrapper) Generation(num int64) *JobWrapper {
	j.ObjectMeta.Generation = num
	return j
}

// Clone returns a deep copy of the job.
func (j *JobWrapper) Clone() *JobWrapper {
	return &JobWrapper{*j.DeepCopy()}
}

// Label sets the label key and value
func (j *JobWrapper) Label(key, value string) *JobWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// JobDeploymentStatus sets a deployment status of the job
func (j *JobWrapper) JobDeploymentStatus(ds rayv1.JobDeploymentStatus) *JobWrapper {
	j.Status.JobDeploymentStatus = ds
	return j
}

// JobStatus sets a status of the job
func (j *JobWrapper) JobStatus(s rayv1.JobStatus) *JobWrapper {
	j.Status.JobStatus = s
	return j
}

// Request adds a resource request to the default container.
func (j *JobWrapper) Request(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *JobWrapper {
	if rayType == rayv1.HeadNode {
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	} else if rayType == rayv1.WorkerNode {
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	}
	return j
}

func (j *JobWrapper) Image(rayType rayv1.RayNodeType, image string, args []string) *JobWrapper {
	if rayType == rayv1.HeadNode {
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Args = args
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	} else if rayType == rayv1.WorkerNode {
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Args = args
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	}
	return j
}

func (j *JobWrapper) Entrypoint(e string) *JobWrapper {
	j.Spec.Entrypoint = e
	return j
}

func (j *JobWrapper) RayVersion(rv string) *JobWrapper {
	j.Spec.RayClusterSpec.RayVersion = rv
	return j
}

func (j *JobWrapper) Env(rayType rayv1.RayNodeType, name, value string) *JobWrapper {
	if rayType == rayv1.HeadNode {
		if j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Env == nil {
			j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Env = make([]corev1.EnvVar, 0)
		}
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Env = append(j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: name, Value: value})
	} else if rayType == rayv1.WorkerNode {
		if j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Env == nil {
			j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Env = make([]corev1.EnvVar, 0)
		}
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Env = append(j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Env, corev1.EnvVar{Name: name, Value: value})
	}
	return j
}
