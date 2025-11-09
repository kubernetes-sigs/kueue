// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sparkapplication

import (
	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

// SparkApplicationWrapper wraps a SparkApplication.
type SparkApplicationWrapper struct {
	sparkappv1beta2.SparkApplication
}

// MakeSparkApplication creates a wrapper for SparkApplication with some default values
func MakeSparkApplication(name, ns string) *SparkApplicationWrapper {
	return &SparkApplicationWrapper{sparkappv1beta2.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: sparkappv1beta2.SparkApplicationSpec{
			Type:                sparkappv1beta2.SparkApplicationTypeScala,
			Mode:                sparkappv1beta2.DeployModeCluster,
			SparkVersion:        "4.0.0",
			Image:               ptr.To("spark:4.0.0"),
			MainApplicationFile: ptr.To("local:///opt/spark/examples/jars/spark-examples.jar"),
			MainClass:           ptr.To("org.apache.spark.examples.SparkPi"),
			Arguments:           []string{"1000"},
			Driver: sparkappv1beta2.DriverSpec{
				SparkPodSpec: sparkappv1beta2.SparkPodSpec{
					Memory:         ptr.To("512Mi"),
					ServiceAccount: ptr.To("spark-operator-spark"),
				},
				CoreRequest: ptr.To("100m"),
			},
			Executor: sparkappv1beta2.ExecutorSpec{
				SparkPodSpec: sparkappv1beta2.SparkPodSpec{
					Memory:         ptr.To("512Mi"),
					ServiceAccount: ptr.To("spark-operator-spark"),
				},
				CoreRequest:         ptr.To("100m"),
				Instances:           ptr.To[int32](1),
				DeleteOnTermination: ptr.To(false),
			},
		},
	}}
}

// Clone creates a deep copy of the SparkApplicationWrapper.
func (w *SparkApplicationWrapper) Clone() *SparkApplicationWrapper {
	clone := w.DeepCopy()
	return &SparkApplicationWrapper{*clone}
}

// Suspend sets the suspend field of the SparkApplication.
func (w *SparkApplicationWrapper) Suspend(suspend bool) *SparkApplicationWrapper {
	w.Spec.Suspend = ptr.To(suspend)
	return w
}

// Label sets the label of the SparkApplication.
func (w *SparkApplicationWrapper) Label(key, value string) *SparkApplicationWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[key] = value
	return w
}

// Annotation sets the annotation of the SparkApplication.
func (w *SparkApplicationWrapper) Annotation(key, value string) *SparkApplicationWrapper {
	if w.Annotations == nil {
		w.Annotations = make(map[string]string)
	}
	w.Annotations[key] = value
	return w
}

// DriverCoreRequest sets the driver core request.
func (w *SparkApplicationWrapper) DriverCoreRequest(q string) *SparkApplicationWrapper {
	w.Spec.Driver.CoreRequest = ptr.To(q)
	return w
}

// DriverMemoryRequest sets the driver memory request.
// Note: the string in Java format, e.g. "512m", "2g".
func (w *SparkApplicationWrapper) DriverMemoryRequest(q string) *SparkApplicationWrapper {
	w.Spec.Driver.Memory = ptr.To(q)
	return w
}

// ExecutorCoreRequest sets the executor core request.
func (w *SparkApplicationWrapper) ExecutorCoreRequest(q string) *SparkApplicationWrapper {
	w.Spec.Executor.CoreRequest = ptr.To(q)
	return w
}

// ExecutorMemoryRequest sets the executor memory request.
// Note: the string in Java format, e.g. "512m", "2g".
func (w *SparkApplicationWrapper) ExecutorMemoryRequest(q string) *SparkApplicationWrapper {
	w.Spec.Executor.Memory = ptr.To(q)
	return w
}

// ExecutorInstances sets the number of executor instances.
func (w *SparkApplicationWrapper) ExecutorInstances(n int32) *SparkApplicationWrapper {
	w.Spec.Executor.Instances = ptr.To(n)
	return w
}

// DynamicAllocation sets the dynamic allocation configuration.
func (w *SparkApplicationWrapper) DynamicAllocation(dynamicAllocation *sparkappv1beta2.DynamicAllocation) *SparkApplicationWrapper {
	w.Spec.DynamicAllocation = dynamicAllocation
	return w
}

// DriverServiceAccount sets the driver service account.
func (w *SparkApplicationWrapper) DriverServiceAccount(sa string) *SparkApplicationWrapper {
	w.Spec.Driver.ServiceAccount = ptr.To(sa)
	return w
}

// DriverAnnotation sets an annotation on the driver pod.
func (w *SparkApplicationWrapper) DriverAnnotation(key, value string) *SparkApplicationWrapper {
	if w.Spec.Driver.Annotations == nil {
		w.Spec.Driver.Annotations = make(map[string]string)
	}
	w.Spec.Driver.Annotations[key] = value
	return w
}

// DriverLabel sets a label on the driver pod.
func (w *SparkApplicationWrapper) DriverLabel(key, value string) *SparkApplicationWrapper {
	if w.Spec.Driver.Labels == nil {
		w.Spec.Driver.Labels = make(map[string]string)
	}
	w.Spec.Driver.Labels[key] = value
	return w
}

// DriverNodeSelector sets a nodeSelector on the driver pod.
func (w *SparkApplicationWrapper) DriverNodeSelector(s map[string]string) *SparkApplicationWrapper {
	w.Spec.Driver.NodeSelector = s
	return w
}

// DriverTolerations sets tolerations on the driver pod.
func (w *SparkApplicationWrapper) DriverTolerations(ts []corev1.Toleration) *SparkApplicationWrapper {
	w.Spec.Driver.Tolerations = ts
	return w
}

// DriverTemplate sets a pod template on the driver pod.
func (w *SparkApplicationWrapper) DriverTemplate(t *corev1.PodTemplateSpec) *SparkApplicationWrapper {
	w.Spec.Driver.Template = t
	return w
}

// ExecutorServiceAccount sets the executor service account.
func (w *SparkApplicationWrapper) ExecutorServiceAccount(sa string) *SparkApplicationWrapper {
	w.Spec.Executor.ServiceAccount = ptr.To(sa)
	return w
}

// ExecutorAnnotation sets an annotation on the executor pod.
func (w *SparkApplicationWrapper) ExecutorAnnotation(key, value string) *SparkApplicationWrapper {
	if w.Spec.Executor.Annotations == nil {
		w.Spec.Executor.Annotations = make(map[string]string)
	}
	w.Spec.Executor.Annotations[key] = value
	return w
}

// ExecutorLabel sets a label on the executor pod.
func (w *SparkApplicationWrapper) ExecutorLabel(key, value string) *SparkApplicationWrapper {
	if w.Spec.Executor.Labels == nil {
		w.Spec.Executor.Labels = make(map[string]string)
	}
	w.Spec.Executor.Labels[key] = value
	return w
}

// ExecutorNodeSelector sets a nodeSelector on the executor pod.
func (w *SparkApplicationWrapper) ExecutorNodeSelector(s map[string]string) *SparkApplicationWrapper {
	w.Spec.Executor.NodeSelector = s
	return w
}

// ExecutorTolerations sets tolerations on the executor pod.
func (w *SparkApplicationWrapper) ExecutorTolerations(ts []corev1.Toleration) *SparkApplicationWrapper {
	w.Spec.Executor.Tolerations = ts
	return w
}

// ExecutorTemplate sets a pod template on the executor pod.
func (w *SparkApplicationWrapper) ExecutorTemplate(t *corev1.PodTemplateSpec) *SparkApplicationWrapper {
	w.Spec.Executor.Template = t
	return w
}

// Queue sets the local queue name in the annotations.
func (w *SparkApplicationWrapper) Queue(lq string) *SparkApplicationWrapper {
	return w.Label(controllerconstants.QueueLabel, lq)
}

// Obj returns the inner SparkApplication.
func (w *SparkApplicationWrapper) Obj() *sparkappv1beta2.SparkApplication {
	return &w.SparkApplication
}
