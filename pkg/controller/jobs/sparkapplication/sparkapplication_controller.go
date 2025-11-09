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
	"maps"
	"slices"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
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
		SetupWebhook:  SetupWebhook,
		JobType:       &sparkv1beta2.SparkApplication{},
		AddToScheme:   sparkv1beta2.AddToScheme,
	}))
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
	driverPodTemplateSpec, err := j.buildDriverPodTemplateSpec()
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
	driverPodInfo := podSetsInfo[0]
	if j.Spec.Driver.Template == nil {
		j.Spec.Driver.Template = emptyDriverPodTemplateSpec.DeepCopy()
	}
	// spec.NodeSelector and spec.driver.nodeSelector is mutually exclusive
	driverNodeSelector := j.Spec.Driver.NodeSelector
	if j.Spec.NodeSelector != nil {
		driverNodeSelector = j.Spec.NodeSelector
	}
	jDriverPodSetInfo := &podset.PodSetInfo{
		Annotations:     j.Spec.Driver.Annotations,
		Labels:          j.Spec.Driver.Labels,
		NodeSelector:    driverNodeSelector,
		Tolerations:     j.Spec.Driver.Tolerations,
		SchedulingGates: j.Spec.Driver.Template.Spec.SchedulingGates,
	}
	if err := jDriverPodSetInfo.Merge(driverPodInfo); err != nil {
		return err
	}
	j.Spec.Driver.Annotations = jDriverPodSetInfo.Annotations
	j.Spec.Driver.Labels = jDriverPodSetInfo.Labels
	if j.Spec.NodeSelector != nil {
		j.Spec.NodeSelector = jDriverPodSetInfo.NodeSelector
	} else {
		j.Spec.Driver.NodeSelector = jDriverPodSetInfo.NodeSelector
	}
	j.Spec.Driver.Tolerations = jDriverPodSetInfo.Tolerations
	j.Spec.Driver.Template.Spec.SchedulingGates = jDriverPodSetInfo.SchedulingGates

	// executors
	executorPodInfo := podSetsInfo[1]
	if j.Spec.Executor.Template == nil {
		j.Spec.Executor.Template = emptyExecutorPodTemplateSpec.DeepCopy()
	}
	// spec.NodeSelector and spec.executor.nodeSelector is mutually exclusive
	executorNodeSelector := j.Spec.Executor.NodeSelector
	if j.Spec.NodeSelector != nil {
		executorNodeSelector = j.Spec.NodeSelector
	}
	jExecutorPodSetInfo := podset.PodSetInfo{
		Annotations:     j.Spec.Executor.Annotations,
		Labels:          j.Spec.Executor.Labels,
		NodeSelector:    executorNodeSelector,
		Tolerations:     j.Spec.Executor.Tolerations,
		SchedulingGates: j.Spec.Executor.Template.Spec.SchedulingGates,
	}
	if err := jExecutorPodSetInfo.Merge(executorPodInfo); err != nil {
		return err
	}
	j.Spec.Executor.Annotations = jExecutorPodSetInfo.Annotations
	j.Spec.Executor.Labels = jExecutorPodSetInfo.Labels
	if j.Spec.NodeSelector != nil {
		j.Spec.NodeSelector = jExecutorPodSetInfo.NodeSelector
	} else {
		j.Spec.Executor.NodeSelector = jExecutorPodSetInfo.NodeSelector
	}
	j.Spec.Executor.Tolerations = jExecutorPodSetInfo.Tolerations
	j.Spec.Executor.Template.Spec.SchedulingGates = jExecutorPodSetInfo.SchedulingGates

	return nil
}

func (j *SparkApplication) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	expectedLength := 2 // driver + executor
	if len(podSetsInfo) != expectedLength {
		return false
	}

	// driver
	var changed bool
	driverPodInfo := podSetsInfo[0]
	if !maps.Equal(j.Spec.Driver.Annotations, driverPodInfo.Annotations) {
		j.Spec.Driver.Annotations = maps.Clone(driverPodInfo.Annotations)
		changed = true
	}
	if !maps.Equal(j.Spec.Driver.Labels, driverPodInfo.Labels) {
		j.Spec.Driver.Labels = maps.Clone(driverPodInfo.Labels)
		changed = true
	}
	if j.Spec.NodeSelector != nil {
		if !maps.Equal(j.Spec.NodeSelector, driverPodInfo.NodeSelector) {
			j.Spec.NodeSelector = maps.Clone(driverPodInfo.NodeSelector)
			changed = true
		}
	} else {
		if !maps.Equal(j.Spec.Driver.NodeSelector, driverPodInfo.NodeSelector) {
			j.Spec.Driver.NodeSelector = maps.Clone(driverPodInfo.NodeSelector)
			changed = true
		}
	}
	if !slices.Equal(j.Spec.Driver.Tolerations, driverPodInfo.Tolerations) {
		j.Spec.Driver.Tolerations = slices.Clone(driverPodInfo.Tolerations)
		changed = true
	}
	if j.Spec.Driver.Template == nil {
		j.Spec.Driver.Template = emptyDriverPodTemplateSpec.DeepCopy()
	}
	if !slices.Equal(j.Spec.Driver.Template.Spec.SchedulingGates, driverPodInfo.SchedulingGates) {
		j.Spec.Driver.Template.Spec.SchedulingGates = slices.Clone(driverPodInfo.SchedulingGates)
		changed = true
	}

	// executors
	executorPodInfo := podSetsInfo[1]
	j.Spec.Executor.Instances = ptr.To(executorPodInfo.Count)
	if !maps.Equal(j.Spec.Executor.Annotations, executorPodInfo.Annotations) {
		j.Spec.Executor.Annotations = maps.Clone(executorPodInfo.Annotations)
		changed = true
	}
	if !maps.Equal(j.Spec.Executor.Labels, executorPodInfo.Labels) {
		j.Spec.Executor.Labels = maps.Clone(executorPodInfo.Labels)
		changed = true
	}
	if j.Spec.NodeSelector != nil {
		if !maps.Equal(j.Spec.NodeSelector, executorPodInfo.NodeSelector) {
			j.Spec.NodeSelector = maps.Clone(executorPodInfo.NodeSelector)
			changed = true
		}
	} else {
		if !maps.Equal(j.Spec.Executor.NodeSelector, executorPodInfo.NodeSelector) {
			j.Spec.Executor.NodeSelector = maps.Clone(executorPodInfo.NodeSelector)
			changed = true
		}
	}
	if !slices.Equal(j.Spec.Executor.Tolerations, executorPodInfo.Tolerations) {
		j.Spec.Executor.Tolerations = slices.Clone(executorPodInfo.Tolerations)
		changed = true
	}
	if j.Spec.Executor.Template == nil {
		j.Spec.Executor.Template = emptyExecutorPodTemplateSpec.DeepCopy()
	}
	if !slices.Equal(j.Spec.Executor.Template.Spec.SchedulingGates, executorPodInfo.SchedulingGates) {
		j.Spec.Executor.Template.Spec.SchedulingGates = slices.Clone(executorPodInfo.SchedulingGates)
		changed = true
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
