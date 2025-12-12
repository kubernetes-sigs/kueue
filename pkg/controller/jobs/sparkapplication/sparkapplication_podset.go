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

package sparkapplication

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	sparkutil "github.com/kubeflow/spark-operator/v2/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	emptyDriverPodTemplateSpec = &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: sparkcommon.SparkDriverContainerName,
			}},
		},
	}
	emptyExecutorPodTemplateSpec = &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: sparkcommon.Spark3DefaultExecutorContainerName,
			}},
		},
	}
)

func (j *SparkApplication) numInitialExecutors() int32 {
	numInitialExecutors := int32(0)
	if j.Spec.Executor.Instances != nil {
		numInitialExecutors = *j.Spec.Executor.Instances
	}
	return numInitialExecutors
}

func (j *SparkApplication) buildDriverPodTemplateSpec() (*corev1.PodTemplateSpec, error) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				sparkcommon.LabelSparkApplicationSelector: j.Name,
				sparkcommon.LabelSparkRole:                sparkcommon.SparkRoleDriver,
			},
		},
		Spec: *emptyDriverPodTemplateSpec.Spec.DeepCopy(),
	}

	if err := mutateSparkPod((*sparkv1beta2.SparkApplication)(j), &pod); err != nil {
		return nil, err
	}

	return &corev1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec:       pod.Spec,
	}, nil
}

func (j *SparkApplication) buildExecutorPodTemplateSpec() (*corev1.PodTemplateSpec, error) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				sparkcommon.LabelSparkApplicationSelector: j.Name,
				sparkcommon.LabelSparkRole:                sparkcommon.SparkRoleExecutor,
			},
		},
		Spec: *emptyExecutorPodTemplateSpec.Spec.DeepCopy(),
	}

	if err := mutateSparkPod((*sparkv1beta2.SparkApplication)(j), &pod); err != nil {
		return nil, err
	}

	return &corev1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec:       pod.Spec,
	}, nil
}

func hasContainer(pod *corev1.Pod, container *corev1.Container) bool {
	return slices.ContainsFunc(pod.Spec.Containers, func(c corev1.Container) bool {
		return container.Name == c.Name && container.Image == c.Image
	})
}

func hasInitContainer(pod *corev1.Pod, container *corev1.Container) bool {
	return slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool {
		return container.Name == c.Name && container.Image == c.Image
	})
}

func findContainer(pod *corev1.Pod) int {
	switch {
	case sparkutil.IsDriverPod(pod):
		// if no containers match the driver container name, assume the first container is the one
		return max(slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
			return c.Name == sparkcommon.SparkDriverContainerName
		}), 0)
	case sparkutil.IsExecutorPod(pod):
		// if no containers match the executor container name, assume the first container is the one
		return max(slices.IndexFunc(pod.Spec.Containers, func(c corev1.Container) bool {
			return c.Name == sparkcommon.SparkExecutorContainerName
		}), 0)
	default:
		return -1
	}
}

// NOTE: Most of the below code is adapted from kubeflow/spark-operator internal code
// ref: https://github.com/kubeflow/spark-operator/blob/v2.3.0/internal/webhook/sparkpod_defaulter.go
func mutateSparkPod(app *sparkv1beta2.SparkApplication, pod *corev1.Pod) error {
	// this focuses on resource related fields only
	options := []mutateSparkPodOption{
		addVolumes,
		addInitContainers,
		addSidecarContainers,
		addPriorityClassName,
		addNodeSelectors,
		addAffinity,
		addTolerations,
		addCPURequests,
		addCPULimit,
		addMemoryRequests,
		addMemoryLimit,
		addGPU,
		addObjectMeta,
	}

	for _, option := range options {
		if err := option(pod, app); err != nil {
			return err
		}
	}

	return nil
}

type mutateSparkPodOption func(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error

func addObjectMeta(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	mergeMetadata := func(base *corev1.PodTemplateSpec, labels, annotations map[string]string) metav1.ObjectMeta {
		if base == nil {
			return metav1.ObjectMeta{
				Labels:      labels,
				Annotations: annotations,
			}
		}

		meta := base.ObjectMeta.DeepCopy()
		if base.Labels == nil {
			meta.Labels = map[string]string{}
		}
		maps.Copy(meta.Labels, labels)

		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		maps.Copy(meta.Annotations, annotations)

		return *meta
	}

	if sparkutil.IsDriverPod(pod) {
		pod.ObjectMeta = mergeMetadata(app.Spec.Driver.Template, app.Spec.Driver.Labels, app.Spec.Driver.Annotations)
	} else if sparkutil.IsExecutorPod(pod) {
		pod.ObjectMeta = mergeMetadata(app.Spec.Executor.Template, app.Spec.Executor.Labels, app.Spec.Executor.Annotations)
	}

	return nil
}

func addVolume(pod *corev1.Pod, volume corev1.Volume) error {
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
	return nil
}

func addVolumeMount(pod *corev1.Pod, mount corev1.VolumeMount) error {
	i := findContainer(pod)
	if i < 0 {
		return errors.New("failed to add volumeMounts as Spark container not found")
	}

	pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, mount)
	return nil
}

func addVolumes(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	volumes := app.Spec.Volumes

	volumeMap := make(map[string]corev1.Volume)
	for _, v := range volumes {
		volumeMap[v.Name] = v
	}

	var volumeMounts []corev1.VolumeMount
	if sparkutil.IsDriverPod(pod) {
		volumeMounts = app.Spec.Driver.VolumeMounts
	} else if sparkutil.IsExecutorPod(pod) {
		volumeMounts = app.Spec.Executor.VolumeMounts
	}

	addedVolumeMap := make(map[string]corev1.Volume)
	for _, m := range volumeMounts {
		// Skip adding localDirVolumes
		if strings.HasPrefix(m.Name, sparkcommon.SparkLocalDirVolumePrefix) {
			continue
		}

		if v, ok := volumeMap[m.Name]; ok {
			if _, ok := addedVolumeMap[m.Name]; !ok {
				_ = addVolume(pod, v)
				addedVolumeMap[m.Name] = v
			}
			_ = addVolumeMount(pod, m)
		}
	}
	return nil
}

func addInitContainers(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var initContainers []corev1.Container
	if sparkutil.IsDriverPod(pod) {
		initContainers = app.Spec.Driver.InitContainers
	} else if sparkutil.IsExecutorPod(pod) {
		initContainers = app.Spec.Executor.InitContainers
	}

	if pod.Spec.InitContainers == nil {
		pod.Spec.InitContainers = []corev1.Container{}
	}

	for _, container := range initContainers {
		if !hasInitContainer(pod, &container) {
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, *container.DeepCopy())
		}
	}
	return nil
}

func addSidecarContainers(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var sidecars []corev1.Container
	if sparkutil.IsDriverPod(pod) {
		sidecars = app.Spec.Driver.Sidecars
	} else if sparkutil.IsExecutorPod(pod) {
		sidecars = app.Spec.Executor.Sidecars
	}

	for _, sidecar := range sidecars {
		if !hasContainer(pod, &sidecar) {
			pod.Spec.Containers = append(pod.Spec.Containers, *sidecar.DeepCopy())
		}
	}
	return nil
}

func addPriorityClassName(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var priorityClassName *string

	if sparkutil.IsDriverPod(pod) {
		priorityClassName = app.Spec.Driver.PriorityClassName
	} else if sparkutil.IsExecutorPod(pod) {
		priorityClassName = app.Spec.Executor.PriorityClassName
	}

	if priorityClassName != nil && *priorityClassName != "" {
		pod.Spec.PriorityClassName = *priorityClassName
		pod.Spec.Priority = nil
		pod.Spec.PreemptionPolicy = nil
	}

	return nil
}

func addNodeSelectors(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var nodeSelector map[string]string

	if sparkutil.IsDriverPod(pod) {
		nodeSelector = app.Spec.Driver.NodeSelector
	} else if sparkutil.IsExecutorPod(pod) {
		nodeSelector = app.Spec.Executor.NodeSelector
	}

	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = make(map[string]string)
	}

	maps.Copy(pod.Spec.NodeSelector, nodeSelector)

	return nil
}

func addAffinity(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var affinity *corev1.Affinity
	if sparkutil.IsDriverPod(pod) {
		affinity = app.Spec.Driver.Affinity
	} else if sparkutil.IsExecutorPod(pod) {
		affinity = app.Spec.Executor.Affinity
	}
	if affinity == nil {
		return nil
	}
	pod.Spec.Affinity = affinity.DeepCopy()
	return nil
}

func addTolerations(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var tolerations []corev1.Toleration
	if sparkutil.IsDriverPod(pod) {
		tolerations = app.Spec.Driver.Tolerations
	} else if sparkutil.IsExecutorPod(pod) {
		tolerations = app.Spec.Executor.Tolerations
	}

	if pod.Spec.Tolerations == nil {
		pod.Spec.Tolerations = []corev1.Toleration{}
	}

	pod.Spec.Tolerations = append(pod.Spec.Tolerations, tolerations...)
	return nil
}

func addCPURequests(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	i := findContainer(pod)
	if i < 0 {
		return fmt.Errorf("failed to add CPU requests as Spark container was not found in pod %s", pod.Name)
	}

	var cpuRequests *string
	if sparkutil.IsDriverPod(pod) {
		cpuRequests = app.Spec.Driver.CoreRequest
	} else if sparkutil.IsExecutorPod(pod) {
		cpuRequests = app.Spec.Executor.CoreRequest
	}

	if cpuRequests == nil {
		return nil
	}

	// Convert CPU requests to a Kubernetes-style unit
	requestsQuantity, err := resource.ParseQuantity(*cpuRequests)
	if err != nil {
		return fmt.Errorf("failed to parse CPU requests %s: %v", *cpuRequests, err)
	}

	if pod.Spec.Containers[i].Resources.Requests == nil {
		pod.Spec.Containers[i].Resources.Requests = corev1.ResourceList{}
	}

	// Apply the CPU requests to the container's resources
	pod.Spec.Containers[i].Resources.Requests[corev1.ResourceCPU] = requestsQuantity
	return nil
}

func addCPULimit(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	i := findContainer(pod)
	if i < 0 {
		return fmt.Errorf("failed to add CPU limit as Spark container was not found in pod %s", pod.Name)
	}

	var cpuLimit *string
	if sparkutil.IsDriverPod(pod) {
		cpuLimit = app.Spec.Driver.CoreLimit
	} else if sparkutil.IsExecutorPod(pod) {
		cpuLimit = app.Spec.Executor.CoreLimit
	}

	if cpuLimit == nil {
		return nil
	}

	// Convert CPU limit to a Kubernetes-style unit
	limitQuantity, err := resource.ParseQuantity(*cpuLimit)
	if err != nil {
		return fmt.Errorf("failed to parse CPU limit %s: %v", *cpuLimit, err)
	}

	if pod.Spec.Containers[i].Resources.Limits == nil {
		pod.Spec.Containers[i].Resources.Limits = corev1.ResourceList{}
	}

	// Apply the CPU limit to the container's resources
	pod.Spec.Containers[i].Resources.Limits[corev1.ResourceCPU] = limitQuantity
	return nil
}

func addMemoryRequests(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	i := findContainer(pod)
	if i < 0 {
		return fmt.Errorf("failed to add memory requests as Spark container was not found in pod %s", pod.Name)
	}

	var memoryRequests *string
	if sparkutil.IsDriverPod(pod) {
		memoryRequests = app.Spec.Driver.Memory
	} else if sparkutil.IsExecutorPod(pod) {
		memoryRequests = app.Spec.Executor.Memory
	}

	if memoryRequests == nil {
		return nil
	}

	// Convert memory requests to a Kubernetes-style unit
	requestsQuantity, err := resource.ParseQuantity(sparkutil.ConvertJavaMemoryStringToK8sMemoryString(*memoryRequests))
	if err != nil {
		return fmt.Errorf("failed to parse memory requests %s: %v", *memoryRequests, err)
	}

	if pod.Spec.Containers[i].Resources.Requests == nil {
		pod.Spec.Containers[i].Resources.Requests = corev1.ResourceList{}
	}

	// Apply the memory requests to the container's resources
	pod.Spec.Containers[i].Resources.Requests[corev1.ResourceMemory] = requestsQuantity
	return nil
}

func addMemoryLimit(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	i := findContainer(pod)
	if i < 0 {
		return fmt.Errorf("failed to add memory limit as Spark container was not found in pod %s", pod.Name)
	}

	var memoryLimit *string
	if sparkutil.IsDriverPod(pod) {
		memoryLimit = app.Spec.Driver.MemoryLimit
	} else if sparkutil.IsExecutorPod(pod) {
		memoryLimit = app.Spec.Executor.MemoryLimit
	}

	if memoryLimit == nil {
		return nil
	}

	// Convert memory limit to a Kubernetes-style unit
	limitQuantity, err := resource.ParseQuantity(sparkutil.ConvertJavaMemoryStringToK8sMemoryString(*memoryLimit))
	if err != nil {
		return fmt.Errorf("failed to parse memory limit %s: %v", *memoryLimit, err)
	}

	if pod.Spec.Containers[i].Resources.Limits == nil {
		pod.Spec.Containers[i].Resources.Limits = corev1.ResourceList{}
	}

	// Apply the memory limit to the container's resources
	pod.Spec.Containers[i].Resources.Limits[corev1.ResourceMemory] = limitQuantity
	return nil
}

func addGPU(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) error {
	var gpu *sparkv1beta2.GPUSpec
	if sparkutil.IsDriverPod(pod) {
		gpu = app.Spec.Driver.GPU
	}
	if sparkutil.IsExecutorPod(pod) {
		gpu = app.Spec.Executor.GPU
	}
	if gpu == nil {
		return nil
	}
	if gpu.Name == "" {
		return nil
	}
	if gpu.Quantity <= 0 {
		return nil
	}

	i := findContainer(pod)
	if i < 0 {
		return fmt.Errorf("failed to add GPU as Spark container was not found in pod %s", pod.Name)
	}
	if pod.Spec.Containers[i].Resources.Limits == nil {
		pod.Spec.Containers[i].Resources.Limits = make(corev1.ResourceList)
	}
	pod.Spec.Containers[i].Resources.Limits[corev1.ResourceName(gpu.Name)] = *resource.NewQuantity(gpu.Quantity, resource.DecimalSI)
	return nil
}
