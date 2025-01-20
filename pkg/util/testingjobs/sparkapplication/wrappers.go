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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"

	kfsparkapi "github.com/kubeflow/spark-operator/api/v1beta2"
	kfsparkcommon "github.com/kubeflow/spark-operator/pkg/common"
)

// SparkApplicationWrapper wraps a SparkApplication.
type SparkApplicationWrapper struct{ kfsparkapi.SparkApplication }

// MakeSparkApplication creates a wrapper for a suspended SparkApplication
func MakeSparkApplication(name, ns string) *SparkApplicationWrapper {
	return &SparkApplicationWrapper{kfsparkapi.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: kfsparkapi.SparkApplicationSpec{
			Suspend:      false,
			Image:        ptr.To("spark:3.5.3"),
			Mode:         kfsparkapi.DeployModeCluster,
			Type:         kfsparkapi.SparkApplicationTypeScala,
			MainClass:    ptr.To("local:///opt/spark/examples/jars/spark-examples.jar"),
			SparkVersion: "3.5.3",
			Driver: kfsparkapi.DriverSpec{
				CoreRequest:       ptr.To("1"),
				PriorityClassName: ptr.To("driver-priority-class"),
				SparkPodSpec: kfsparkapi.SparkPodSpec{
					CoreLimit:      ptr.To("1"),
					Memory:         ptr.To("256Mi"),
					SchedulerName:  ptr.To("test"),
					ServiceAccount: ptr.To("test"),
					Labels: map[string]string{
						"spec-driver-labels": "spec-driver-labels",
					},
					Annotations: map[string]string{
						"spec-driver-annotations": "spec-driver-annotations",
					},
				},
			},
			Executor: kfsparkapi.ExecutorSpec{
				Instances:         nil,
				CoreRequest:       ptr.To("1"),
				PriorityClassName: ptr.To("executor-priority-class"),
				SparkPodSpec: kfsparkapi.SparkPodSpec{
					Template: &corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"spec-executor-template-labels": "spec-executor-template-labels",
							},
						},
					},
					CoreLimit: ptr.To("1"),
					Memory:    ptr.To("256Mi"),
					GPU: &kfsparkapi.GPUSpec{
						Name:     "nvidia.com/gpu",
						Quantity: 1,
					},
					Image: ptr.To("executor-image"),
					Labels: map[string]string{
						"spec-executor-labels": "spec-executor-labels",
					},
					Annotations: map[string]string{
						"spec-executor-annotations": "spec-executor-annotations",
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
								Weight: 1,
								Preference: corev1.NodeSelectorTerm{
									MatchFields: []corev1.NodeSelectorRequirement{{
										Key:      "test",
										Operator: corev1.NodeSelectorOpExists,
									}},
								},
							}},
						},
					},
					Tolerations: []corev1.Toleration{{
						Key:      "test",
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					}},
					SchedulerName:  ptr.To("test"),
					Sidecars:       []corev1.Container{{Name: "sidecar", Image: "test"}},
					InitContainers: []corev1.Container{{Name: "initContainer", Image: "test"}},
					NodeSelector: map[string]string{
						"spec-driver-node-selector": "spec-driver-node-selector",
					},
					ServiceAccount: ptr.To("test"),
				},
			},
		},
	}}
}

// Obj returns the inner Job.
func (w *SparkApplicationWrapper) Obj() *kfsparkapi.SparkApplication {
	return &w.SparkApplication
}

// Suspend updates the suspend status of the SparkApplication
func (w *SparkApplicationWrapper) Suspend(s bool) *SparkApplicationWrapper {
	w.Spec.Suspend = s
	return w
}

// Queue updates the queue name of the SparkApplication
func (w *SparkApplicationWrapper) Queue(queue string) *SparkApplicationWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[constants.QueueLabel] = queue
	return w
}

// ExecutorInstances updates the number of executor instances of the SparkApplication
func (w *SparkApplicationWrapper) ExecutorInstances(n int32) *SparkApplicationWrapper {
	w.Spec.Executor.Instances = &n
	return w
}

// DynamicAllocation explicitly set enabled to dynamic allocation of the SparkApplication
func (w *SparkApplicationWrapper) DynamicAllocation(enabled bool) *SparkApplicationWrapper {
	w.Spec.DynamicAllocation = &kfsparkapi.DynamicAllocation{Enabled: enabled}
	return w
}

// DynamicAllocationMaxExecutor explicitly set  dynamicAllocation.maxExecutors of the SparkApplication
func (w *SparkApplicationWrapper) DynamicAllocationMaxExecutor(num int32) *SparkApplicationWrapper {
	if w.Spec.DynamicAllocation == nil {
		w.Spec.DynamicAllocation = &kfsparkapi.DynamicAllocation{}
	}
	w.Spec.DynamicAllocation.MaxExecutors = &num
	return w
}

// Label sets the label key and value
func (w *SparkApplicationWrapper) Label(key, value string) *SparkApplicationWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[key] = value
	return w
}

// WorkloadPriorityClass updates SparkApplication workloadpriorityclass.
func (w *SparkApplicationWrapper) WorkloadPriorityClass(wpc string) *SparkApplicationWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[constants.WorkloadPriorityClassLabel] = wpc
	return w
}

// DriverAnnotation sets the annotation to Spec.Driver.Annotations
func (w *SparkApplicationWrapper) DriverAnnotation(key, value string) *SparkApplicationWrapper {
	if w.Spec.Driver.Annotations == nil {
		w.Spec.Driver.Annotations = make(map[string]string, 1)
	}
	w.Spec.Driver.Annotations[key] = value
	return w
}

// ExecutorAnnotation sets the annotation to Spec.Executor.Annotations
func (w *SparkApplicationWrapper) ExecutorAnnotation(key, value string) *SparkApplicationWrapper {
	if w.Spec.Executor.Annotations == nil {
		w.Spec.Executor.Annotations = make(map[string]string, 1)
	}
	w.Spec.Executor.Annotations[key] = value
	return w
}

// DriverLabel sets the annotation to Spec.Driver.Labels
func (w *SparkApplicationWrapper) DriverLabel(key, value string) *SparkApplicationWrapper {
	if w.Spec.Driver.Labels == nil {
		w.Spec.Driver.Labels = make(map[string]string, 1)
	}
	w.Spec.Driver.Labels[key] = value
	return w
}

// ExecutorLabel sets the annotation to Spec.Executor.Labels
func (w *SparkApplicationWrapper) ExecutorLabel(key, value string) *SparkApplicationWrapper {
	if w.Spec.Executor.Labels == nil {
		w.Spec.Executor.Labels = make(map[string]string, 1)
	}
	w.Spec.Executor.Labels[key] = value
	return w
}

// CoreLimit sets the driver or executor spec.CoreLimit.
func (w *SparkApplicationWrapper) CoreLimit(role string, quantity *string) *SparkApplicationWrapper {
	switch role {
	case kfsparkcommon.SparkRoleDriver:
		w.Spec.Driver.CoreLimit = quantity
	case kfsparkcommon.SparkRoleExecutor:
		w.Spec.Executor.CoreLimit = quantity
	}
	return w
}

// Clone clones the SparkApplicationWrapper
func (w *SparkApplicationWrapper) Clone() *SparkApplicationWrapper {
	return &SparkApplicationWrapper{SparkApplication: *w.DeepCopy()}
}
