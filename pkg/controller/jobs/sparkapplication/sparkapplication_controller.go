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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
)

var (
	gvk = sparkv1beta2.GroupVersion.WithKind("SparkApplication")
)

const (
	FrameworkName      = "sparkoperator.k8s.io/sparkapplication"
	driverPodSetName   = "driver"
	executorPodSetName = "executor"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewJob:        NewJob,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupWebhooks,
		JobType:       &sparkv1beta2.SparkApplication{},
		AddToScheme:   sparkv1beta2.AddToScheme,
	}))
}

// SetupWebhooks configures the webhook for sparkappv1beta2 SparkApplication integration.
func SetupWebhooks(mgr ctrl.Manager, opts ...jobframework.Option) error {
	if err := setupSparkApplicationWebhook(mgr, opts...); err != nil {
		return fmt.Errorf("failed setting up SparkApplication webhook: %w", err)
	}
	if err := setupSparkApplicationLaunchedExecutorPodWebhook(mgr, opts...); err != nil {
		return fmt.Errorf("failed setting up SparkApplication executor Pod webhook: %w", err)
	}
	return nil
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &SparkApplication{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type SparkApplication sparkv1beta2.SparkApplication

var _ jobframework.GenericJob = (*SparkApplication)(nil)

func (j *SparkApplication) Object() client.Object {
	return (*sparkv1beta2.SparkApplication)(j)
}

func (j *SparkApplication) IsSuspended() bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

func (j *SparkApplication) IsActive() bool {
	return j.Status.AppState.State == sparkv1beta2.ApplicationStateRunning
}

func (j *SparkApplication) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *SparkApplication) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *SparkApplication) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", sparkcommon.LabelSparkAppName, j.Name)
}

func (j *SparkApplication) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	// driver and executor
	podSets := make([]kueue.PodSet, 2)

	// driver
	driverPodTemplateSpec, err := j.buildDriverPodSet()
	if err != nil {
		return nil, err
	}
	podSets[0] = kueue.PodSet{
		Name:     driverPodSetName,
		Template: *driverPodTemplateSpec,
		Count:    1,
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&driverPodTemplateSpec.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		podSets[0].TopologyRequest = topologyRequest
	}

	// executors
	executorPodTemplateSpec, err := j.buildExecutorPodTemplateSpec()
	if err != nil {
		return nil, err
	}
	podSets[1] = kueue.PodSet{
		Name:     executorPodSetName,
		Template: *executorPodTemplateSpec,
		Count:    j.numInitialExecutors(),
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&executorPodTemplateSpec.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		podSets[1].TopologyRequest = topologyRequest
	}

	return podSets, nil
}

func (j *SparkApplication) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := 2 // driver + executor
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = ptr.To(false)

	// driver
	driverPodTemplateSpec, err := j.buildDriverPodSet()
	if err != nil {
		return err
	}
	driverPodInfo := podSetsInfo[0]
	if err := podset.Merge(&driverPodTemplateSpec.ObjectMeta, &driverPodTemplateSpec.Spec, driverPodInfo); err != nil {
		return err
	}

	// executors
	executorPodTemplateSpec, err := j.buildExecutorPodTemplateSpec()
	if err != nil {
		return err
	}
	executorPodInfo := podSetsInfo[1]
	if err := podset.Merge(&executorPodTemplateSpec.ObjectMeta, &executorPodTemplateSpec.Spec, executorPodInfo); err != nil {
		return err
	}

	return nil
}

func (j *SparkApplication) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	expectedLength := 2 // driver + executor
	if len(podSetsInfo) != expectedLength {
		return false
	}

	// driver
	var changed bool
	if driverPodTemplateSpec, _ := j.buildDriverPodSet(); driverPodTemplateSpec != nil {
		changed = podset.RestorePodSpec(&driverPodTemplateSpec.ObjectMeta, &driverPodTemplateSpec.Spec, podSetsInfo[0])
	}
	// executors
	if executorPodTemplateSpec, _ := j.buildExecutorPodTemplateSpec(); executorPodTemplateSpec != nil {
		changed = podset.RestorePodSpec(&executorPodTemplateSpec.ObjectMeta, &executorPodTemplateSpec.Spec, podSetsInfo[1])
	}

	return changed
}

func (j *SparkApplication) Finished(ctx context.Context) (message string, success, finished bool) {
	return j.Status.AppState.ErrorMessage,
		j.Status.AppState.State == sparkv1beta2.ApplicationStateCompleted,
		j.Status.AppState.State == sparkv1beta2.ApplicationStateCompleted ||
			j.Status.AppState.State == sparkv1beta2.ApplicationStateFailed ||
			j.Status.AppState.State == sparkv1beta2.ApplicationStateFailedSubmission

}

func (j *SparkApplication) PodsReady(ctx context.Context) bool {
	return j.Status.AppState.State == sparkv1beta2.ApplicationStateRunning
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForSparkApplication(sparkAppName string, sparkAppUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(sparkAppName, sparkAppUID, gvk)
}

func FromObject(o runtime.Object) *SparkApplication {
	return (*SparkApplication)(o.(*sparkv1beta2.SparkApplication))
}
