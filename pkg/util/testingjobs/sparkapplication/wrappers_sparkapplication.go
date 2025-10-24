package sparkapplication

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"

	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
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
			SparkVersion:        "3.5.3",
			Image:               ptr.To("spark:3.5.3"),
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

// Label sets the label of the SparkApplication.
func (w *SparkApplicationWrapper) Label(key, value string) *SparkApplicationWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[key] = value
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

// DriverServiceAccount sets the driver service account.
func (w *SparkApplicationWrapper) DriverServiceAccount(sa string) *SparkApplicationWrapper {
	w.Spec.Driver.ServiceAccount = ptr.To(sa)
	return w
}

// ExecutorServiceAccount sets the executor service account.
func (w *SparkApplicationWrapper) ExecutorServiceAccount(sa string) *SparkApplicationWrapper {
	w.Spec.Executor.ServiceAccount = ptr.To(sa)
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
