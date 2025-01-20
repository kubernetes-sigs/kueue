/*
Copyright 2025 The Kubernetes Authors.

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
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"

	kfsparkapi "github.com/kubeflow/spark-operator/api/v1beta2"
	kfsparkcommon "github.com/kubeflow/spark-operator/pkg/common"
	kfsparkutil "github.com/kubeflow/spark-operator/pkg/util"
)

var (
	gvk = kfsparkapi.SchemeGroupVersion.WithKind("SparkApplication")

	FrameworkName = "sparkoperator.k8s.io/sparkapplication"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewJob:                 NewJob,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupSparkApplicationWebhook,
		JobType:                &kfsparkapi.SparkApplication{},
		AddToScheme:            kfsparkapi.AddToScheme,
		IsManagingObjectsOwner: isSparkApplication,
		DependencyList:         []string{"pod"},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update

func NewJob() jobframework.GenericJob {
	return &SparkApplication{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type SparkApplication kfsparkapi.SparkApplication

type integrationMode string

const (
	integrationModeAggregated       integrationMode = "aggregated"
	integrationModeExecutorDetached integrationMode = "executor-detached"
	integrationModeUnknown          integrationMode = "unknown"
)

var _ jobframework.GenericJob = (*SparkApplication)(nil)

func fromObject(obj runtime.Object) *SparkApplication {
	return (*SparkApplication)(obj.(*kfsparkapi.SparkApplication))
}

func (s *SparkApplication) Finished() (string, bool, bool) {
	failed := s.Status.AppState.State == kfsparkapi.ApplicationStateFailed
	success := s.Status.AppState.State == kfsparkapi.ApplicationStateCompleted
	finished := success || failed
	if finished {
		if failed {
			return s.Status.AppState.ErrorMessage, false, true
		}
		return "", true, true
	}
	return "", false, false
}

func (s *SparkApplication) GVK() schema.GroupVersionKind {
	return gvk
}

func (s *SparkApplication) IsActive() bool {
	active := false
	if s.Status.DriverInfo.PodName != "" {
		active = true
	}
	mode, _ := s.integrationMode()
	if mode == integrationModeAggregated {
		numNonTerminatedPods := 0
		for _, state := range s.Status.ExecutorState {
			if !kfsparkutil.IsExecutorTerminated(state) {
				numNonTerminatedPods++
			}
		}
		active = (numNonTerminatedPods > 0) || active
	}
	return active
}

func (s *SparkApplication) IsSuspended() bool {
	return s.Spec.Suspend
}

func (s *SparkApplication) Object() client.Object {
	return (*kfsparkapi.SparkApplication)(s)
}

func (s *SparkApplication) PodSets() ([]kueue.PodSet, error) {
	driverPodSet, err := s.driverPodSet()
	if err != nil {
		return nil, err
	}

	mode, integrationModeErr := s.integrationMode()
	if integrationModeErr != nil {
		return nil, integrationModeErr
	}

	switch mode {
	case integrationModeAggregated:
		executorPodSet, err := s.executorPodSet()
		if err != nil {
			return nil, err
		}
		return []kueue.PodSet{*driverPodSet, *executorPodSet}, nil
	case integrationModeExecutorDetached:
		return []kueue.PodSet{*driverPodSet}, nil
	default:
		return nil, errors.New("it should never happen")
	}
}

func (s *SparkApplication) PodsReady() bool {
	driverReady := kfsparkutil.IsDriverRunning(s.asSparkApp())
	if !driverReady {
		return false
	}

	switch mode, _ := s.integrationMode(); mode {
	case integrationModeAggregated:
		for _, executorState := range s.Status.ExecutorState {
			if executorState != kfsparkapi.ExecutorStateRunning {
				return false
			}
		}
		return true
	case integrationModeExecutorDetached:
		return true
	default:
		return false
	}
}

func (s *SparkApplication) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) == 0 {
		return false
	}
	changed := false
	for _, info := range podSetsInfo {
		if info.Name == kfsparkcommon.SparkRoleDriver {
			ps, err := s.driverPodSet()
			if err != nil {
				continue
			}
			changed = podset.RestorePodSpec(&ps.Template.ObjectMeta, &ps.Template.Spec, info) || changed
			continue
		}

		mode, _ := s.integrationMode()
		if info.Name == kfsparkcommon.SparkRoleExecutor && mode == integrationModeAggregated {
			ps, err := s.executorPodSet()
			if err != nil {
				continue
			}
			changed = podset.RestorePodSpec(&ps.Template.ObjectMeta, &ps.Template.Spec, info) || changed
			continue
		}
	}
	return changed
}

func (s *SparkApplication) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	s.Spec.Suspend = false
	for _, info := range podSetsInfo {
		if info.Name == kfsparkcommon.SparkRoleDriver {
			ps, err := s.driverPodSet()
			if err != nil {
				return err
			}
			if err := podset.Merge(&ps.Template.ObjectMeta, &ps.Template.Spec, info); err != nil {
				return nil
			}
			continue
		}

		mode, _ := s.integrationMode()
		if info.Name == kfsparkcommon.SparkRoleExecutor && mode == integrationModeAggregated {
			ps, err := s.executorPodSet()
			if err != nil {
				return err
			}
			if err := podset.Merge(&ps.Template.ObjectMeta, &ps.Template.Spec, info); err != nil {
				return nil
			}
			continue
		}
	}
	return nil
}

func (s *SparkApplication) Suspend() {
	s.Spec.Suspend = true
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func isSparkApplication(owner *metav1.OwnerReference) bool {
	return owner.Kind == "SparkApplication" && strings.HasPrefix(owner.APIVersion, kfsparkapi.GroupVersion.Group)
}

func (s *SparkApplication) asSparkApp() *kfsparkapi.SparkApplication {
	return (*kfsparkapi.SparkApplication)(s)
}

func (s *SparkApplication) integrationMode() (integrationMode, *field.Error) {
	if s.Spec.DynamicAllocation == nil {
		return integrationModeUnknown, field.Required(
			field.NewPath("spec", "dynamicAllocation"),
			"kueue managed sparkapplication must set spec.dynamicAllocation explicitly (must not be nil)",
		)
	}
	if s.Spec.DynamicAllocation.Enabled {
		return integrationModeExecutorDetached, nil
	} else {
		return integrationModeAggregated, nil
	}
}

func (s *SparkApplication) driverPodSet() (*kueue.PodSet, error) {
	podTemplate, containerIndex, err := sparkPodSpecToPodSetTemplate(s.Spec.Driver.SparkPodSpec, field.NewPath("spec", "driver"), kfsparkcommon.SparkDriverContainerName)
	if err != nil {
		return nil, err
	}
	applySparkSpecFieldsToPodTemplate(s.Spec, containerIndex, podTemplate)

	// DriverSpec
	// TODO: apply other fields in the spec.
	// But, there is no problems because these fields are not interested in PodSet.
	if s.Spec.Driver.CoreRequest != nil {
		if q, err := resource.ParseQuantity(*s.Spec.Driver.CoreRequest); err != nil {
			return nil, fmt.Errorf("spec.driver.coreRequest=%s can't parse: %w", *s.Spec.Driver.CoreRequest, err)
		} else {
			podTemplate.Spec.Containers[containerIndex].Resources.Requests[corev1.ResourceCPU] = q
		}
	}
	if s.Spec.Driver.PriorityClassName != nil {
		podTemplate.Spec.PriorityClassName = *s.Spec.Driver.PriorityClassName
	}

	return &kueue.PodSet{
		Name:            kfsparkcommon.SparkRoleDriver,
		Count:           int32(1),
		Template:        *podTemplate,
		TopologyRequest: jobframework.PodSetTopologyRequest(&podTemplate.ObjectMeta, nil, nil, nil),
	}, nil
}

func (s *SparkApplication) executorPodSet() (*kueue.PodSet, error) {
	podTemplate, containerIndex, err := sparkPodSpecToPodSetTemplate(s.Spec.Executor.SparkPodSpec, field.NewPath("spec", "executor"), kfsparkcommon.SparkExecutorContainerName)
	if err != nil {
		return nil, err
	}

	applySparkSpecFieldsToPodTemplate(s.Spec, containerIndex, podTemplate)

	// Executor Spec
	// TODO: apply other fields in the spec.
	// But, there is no problems because these fields are not interested in PodSet.
	if s.Spec.Executor.CoreRequest != nil {
		if q, err := resource.ParseQuantity(*s.Spec.Executor.CoreRequest); err != nil {
			// TODO: Log
		} else {
			podTemplate.Spec.Containers[containerIndex].Resources.Requests[corev1.ResourceCPU] = q
		}
	}
	if s.Spec.Executor.PriorityClassName != nil {
		podTemplate.Spec.PriorityClassName = *s.Spec.Executor.PriorityClassName
	}
	return &kueue.PodSet{
		Name:            kfsparkcommon.SparkRoleExecutor,
		Template:        *podTemplate,
		Count:           s.executorCount(),
		TopologyRequest: jobframework.PodSetTopologyRequest(&podTemplate.ObjectMeta, ptr.To(kfsparkcommon.LabelSparkExecutorID), nil, nil),
	}, nil
}

func sparkPodSpecToPodSetTemplate(sparkPodSpec kfsparkapi.SparkPodSpec, field *field.Path, targetContainerName string) (*corev1.PodTemplateSpec, int, error) {
	spec, index, err := sparkPodSpecToPodSpec(sparkPodSpec, field, targetContainerName)
	if err != nil {
		return nil, -1, err
	}

	return &corev1.PodTemplateSpec{
		ObjectMeta: sparkPodSpecToMetadata(sparkPodSpec),
		Spec:       *spec,
	}, index, nil
}

func sparkPodSpecToMetadata(sparkPodSpec kfsparkapi.SparkPodSpec) metav1.ObjectMeta {
	podTemplate := &corev1.PodTemplateSpec{}

	if sparkPodSpec.Template != nil {
		podTemplate = sparkPodSpec.Template
	}
	for k, v := range sparkPodSpec.Labels {
		if podTemplate.Labels == nil {
			podTemplate.Labels = map[string]string{}
		}
		podTemplate.Labels[k] = v
	}

	for k, v := range sparkPodSpec.Annotations {
		if podTemplate.Annotations == nil {
			podTemplate.Annotations = map[string]string{}
		}
		podTemplate.Annotations[k] = v
	}

	return podTemplate.ObjectMeta
}

func sparkPodSpecToPodSpec(sparkPodSpec kfsparkapi.SparkPodSpec, field *field.Path, targetContainerName string) (*corev1.PodSpec, int, error) {
	podTemplate := &corev1.PodTemplateSpec{}

	if sparkPodSpec.Template != nil {
		podTemplate = sparkPodSpec.Template
	}

	containerIndex := findContainer(&podTemplate.Spec, targetContainerName)
	if containerIndex == -1 {
		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, corev1.Container{
			Name: targetContainerName,
		})
		containerIndex = len(podTemplate.Spec.Containers) - 1
	}

	if sparkPodSpec.CoreLimit != nil {
		if q, err := resource.ParseQuantity(*sparkPodSpec.CoreLimit); err != nil {
			return nil, -1, fmt.Errorf("%s=%s can't parse: %w", field.Child("coreLimit"), *sparkPodSpec.CoreLimit, err)
		} else {
			if podTemplate.Spec.Containers[containerIndex].Resources.Limits == nil {
				podTemplate.Spec.Containers[containerIndex].Resources.Limits = corev1.ResourceList{}
			}
			podTemplate.Spec.Containers[containerIndex].Resources.Limits[corev1.ResourceCPU] = q
		}
	}

	if sparkPodSpec.Memory != nil {
		if q, err := resource.ParseQuantity(*sparkPodSpec.Memory); err != nil {
			return nil, -1, fmt.Errorf("%s=%s can't parse: %w", field.Child("memory"), *sparkPodSpec.Memory, err)
		} else {
			if podTemplate.Spec.Containers[containerIndex].Resources.Requests == nil {
				podTemplate.Spec.Containers[containerIndex].Resources.Requests = corev1.ResourceList{}
			}
			podTemplate.Spec.Containers[containerIndex].Resources.Requests[corev1.ResourceMemory] = q
		}
	}

	if sparkPodSpec.GPU != nil {
		qStr := fmt.Sprintf("%d", sparkPodSpec.GPU.Quantity)
		if q, err := resource.ParseQuantity(qStr); err != nil {
			return nil, -1, fmt.Errorf("%s=%s can't parse: %w", field.Child("gpu", "quantity"), qStr, err)
		} else {
			podTemplate.Spec.Containers[containerIndex].Resources.Limits[corev1.ResourceName(sparkPodSpec.GPU.Name)] = q
		}
	}

	if sparkPodSpec.Image != nil {
		podTemplate.Spec.Containers[containerIndex].Image = *sparkPodSpec.Image
	}

	if sparkPodSpec.Affinity != nil {
		podTemplate.Spec.Affinity = sparkPodSpec.Affinity
	}

	if sparkPodSpec.Tolerations != nil {
		podTemplate.Spec.Tolerations = sparkPodSpec.Tolerations
	}

	if len(sparkPodSpec.Sidecars) > 0 {
		podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, sparkPodSpec.Sidecars...)
	}

	if len(sparkPodSpec.InitContainers) > 0 {
		podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, sparkPodSpec.InitContainers...)
	}

	if sparkPodSpec.NodeSelector != nil {
		podTemplate.Spec.NodeSelector = sparkPodSpec.NodeSelector
	}

	// TODO: apply other fields in SparkPodSpec.
	// But, there is no problems because these fields are not interested in PodSet.

	return &podTemplate.Spec, containerIndex, nil
}

func applySparkSpecFieldsToPodTemplate(spec kfsparkapi.SparkApplicationSpec, targetContainerIndex int, podTemplate *corev1.PodTemplateSpec) {
	if spec.Image != nil && podTemplate.Spec.Containers[targetContainerIndex].Image == "" {
		podTemplate.Spec.Containers[targetContainerIndex].Image = *spec.Image
	}
	if spec.NodeSelector != nil && podTemplate.Spec.NodeSelector == nil {
		podTemplate.Spec.NodeSelector = spec.NodeSelector
	}

	// TODO: apply other fields in SparkApplicationSpec.
	// But, there is no problems because these fields are not interested in PodSet.
}

func (s *SparkApplication) executorCount() int32 {
	return ptr.Deref(s.Spec.Executor.Instances, 1)
}

func findContainer(podSpec *corev1.PodSpec, containerName string) int {
	for i, c := range podSpec.Containers {
		if c.Name == containerName {
			return i
		}
	}
	return -1
}
