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

package rayservice

import (
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// ServiceWrapper wraps a RayService.
type ServiceWrapper struct{ rayv1.RayService }

// MakeService creates a wrapper for a suspended RayService
func MakeService(name, ns string) *ServiceWrapper {
	return &ServiceWrapper{rayv1.RayService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: rayv1.RayServiceSpec{
			RayClusterSpec: rayv1.RayClusterSpec{
				RayVersion: utiltesting.TestRayVersion(),
				Suspend:    ptr.To(true),
				HeadGroupSpec: rayv1.HeadGroupSpec{
					RayStartParams: map[string]string{},
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{},
							Containers: []corev1.Container{
								{
									Name:    "ray-head",
									Command: []string{},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{},
										Limits:   corev1.ResourceList{},
									},
								},
							},
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
								NodeSelector: map[string]string{},
								Containers: []corev1.Container{
									{
										Name:    "ray-worker",
										Command: []string{},
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{},
											Limits:   corev1.ResourceList{},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}}
}

// Obj returns the inner RayService.
func (j *ServiceWrapper) Obj() *rayv1.RayService {
	return &j.RayService
}

// Suspend updates the suspend status of the RayService
func (j *ServiceWrapper) Suspend(s bool) *ServiceWrapper {
	j.Spec.RayClusterSpec.Suspend = ptr.To(s)
	return j
}

// Queue updates the queue name of the RayService
func (j *ServiceWrapper) Queue(queue string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.QueueLabel] = queue
	return j
}

// Request adds a resource request to the default container.
func (j *ServiceWrapper) Request(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	}
	return j
}

// Limit adds a resource limit to the default container.
func (j *ServiceWrapper) Limit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(v)
	}
	return j
}

// RequestAndLimit adds a resource request and limit to the default container.
func (j *ServiceWrapper) RequestAndLimit(rayType rayv1.RayNodeType, r corev1.ResourceName, v string) *ServiceWrapper {
	return j.Request(rayType, r, v).Limit(rayType, r, v)
}

// Image sets the image for the specified ray node type.
func (j *ServiceWrapper) Image(rayType rayv1.RayNodeType, image string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Image = image
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	}
	return j
}

// Env sets the environment for the specified ray node type.
func (j *ServiceWrapper) Env(rayType rayv1.RayNodeType, env []corev1.EnvVar) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].Env = env
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Env = env
	}
	return j
}

// Volumes sets the volumes for the specified ray node type.
func (j *ServiceWrapper) Volumes(rayType rayv1.RayNodeType, volumes []corev1.Volume) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Volumes = volumes
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Volumes = volumes
	}
	return j
}

// VolumeMounts sets the VolumeMounts for the specified ray node type.
func (j *ServiceWrapper) VolumeMounts(rayType rayv1.RayNodeType, volumeMounts []corev1.VolumeMount) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.Containers[0].VolumeMounts = volumeMounts
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.Containers[0].VolumeMounts = volumeMounts
	}
	return j
}

// RayStartParams sets a start param
func (j *ServiceWrapper) RayStartParam(rayType rayv1.RayNodeType, key, value string) *ServiceWrapper {
	switch rayType {
	case rayv1.HeadNode:
		j.Spec.RayClusterSpec.HeadGroupSpec.RayStartParams[key] = value
	case rayv1.WorkerNode:
		j.Spec.RayClusterSpec.WorkerGroupSpecs[0].RayStartParams[key] = value
	}
	return j
}

// WithServeConfigV2 sets the serve config for the RayService.
func (j *ServiceWrapper) WithServeConfigV2(config string) *ServiceWrapper {
	j.Spec.ServeConfigV2 = config
	return j
}

// Clone returns a deep copy of the RayService.
func (j *ServiceWrapper) Clone() *ServiceWrapper {
	return &ServiceWrapper{*j.DeepCopy()}
}

// Label sets the label key and value
func (j *ServiceWrapper) Label(key, value string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[key] = value
	return j
}

// Annotation sets the annotation key and value
func (j *ServiceWrapper) Annotation(key, value string) *ServiceWrapper {
	if j.Annotations == nil {
		j.Annotations = make(map[string]string)
	}
	j.Annotations[key] = value
	return j
}

// WorkloadPriorityClass updates RayService workloadpriorityclass.
func (j *ServiceWrapper) WorkloadPriorityClass(wpc string) *ServiceWrapper {
	if j.Labels == nil {
		j.Labels = make(map[string]string)
	}
	j.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return j
}

// WithWorkerGroups sets the worker groups.
func (j *ServiceWrapper) WithWorkerGroups(workers ...rayv1.WorkerGroupSpec) *ServiceWrapper {
	j.Spec.RayClusterSpec.WorkerGroupSpecs = workers
	return j
}

// WithHeadGroupSpec sets the head group spec.
func (j *ServiceWrapper) WithHeadGroupSpec(value rayv1.HeadGroupSpec) *ServiceWrapper {
	j.Spec.RayClusterSpec.HeadGroupSpec = value
	return j
}

// RayVersion sets the Ray version.
func (j *ServiceWrapper) RayVersion(rv string) *ServiceWrapper {
	j.Spec.RayClusterSpec.RayVersion = rv
	return j
}
