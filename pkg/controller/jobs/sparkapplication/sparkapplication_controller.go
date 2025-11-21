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
	corev1 "k8s.io/api/core/v1"
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

// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=get;list;watch;update;patch;delete

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

	mutatePodSetInfoFor := func(role string) error {
		var podSetInfo podset.PodSetInfo
		var nodeSelector map[string]string
		var sparkPodSpec *sparkv1beta2.SparkPodSpec

		switch role {
		case sparkcommon.SparkRoleDriver:
			podSetInfo = podSetsInfo[0]
			if j.Spec.Driver.Template == nil {
				j.Spec.Driver.Template = emptyDriverPodTemplateSpec.DeepCopy()
			}
			// spec.NodeSelector and spec.driver.nodeSelector is mutually exclusive
			nodeSelector = j.Spec.Driver.NodeSelector
			if j.Spec.NodeSelector != nil {
				nodeSelector = j.Spec.NodeSelector
			}
			sparkPodSpec = &j.Spec.Driver.SparkPodSpec
		case sparkcommon.SparkRoleExecutor:
			podSetInfo = podSetsInfo[1]
			sparkPodSpec = &j.Spec.Executor.SparkPodSpec
			if j.Spec.Executor.Template == nil {
				j.Spec.Executor.Template = emptyExecutorPodTemplateSpec.DeepCopy()
			}
			// spec.NodeSelector and spec.executor.nodeSelector is mutually exclusive
			nodeSelector = j.Spec.Executor.NodeSelector
			if j.Spec.NodeSelector != nil {
				nodeSelector = j.Spec.NodeSelector
			}
		default:
			return fmt.Errorf("unknown Spark role: %s", role)
		}

		sparkPodSetInfo := &podset.PodSetInfo{
			Annotations:     sparkPodSpec.Annotations,
			Labels:          sparkPodSpec.Labels,
			NodeSelector:    nodeSelector,
			Tolerations:     sparkPodSpec.Tolerations,
			SchedulingGates: sparkPodSpec.Template.Spec.SchedulingGates,
		}
		if err := sparkPodSetInfo.Merge(podSetInfo); err != nil {
			return err
		}
		sparkPodSpec.Annotations = sparkPodSetInfo.Annotations
		sparkPodSpec.Labels = sparkPodSetInfo.Labels
		if j.Spec.NodeSelector != nil {
			j.Spec.NodeSelector = sparkPodSetInfo.NodeSelector
		} else {
			sparkPodSpec.NodeSelector = sparkPodSetInfo.NodeSelector
		}
		sparkPodSpec.Tolerations = sparkPodSetInfo.Tolerations
		sparkPodSpec.Template.Spec.SchedulingGates = sparkPodSetInfo.SchedulingGates
		return nil
	}

	if err := mutatePodSetInfoFor(sparkcommon.SparkRoleDriver); err != nil {
		return err
	}
	if err := mutatePodSetInfoFor(sparkcommon.SparkRoleExecutor); err != nil {
		return err
	}

	return nil
}

func (j *SparkApplication) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	expectedLength := 2 // driver + executor
	if len(podSetsInfo) != expectedLength {
		return false
	}

	restorePodSetsInfoFrom := func(role string) bool {
		var podSetInfo podset.PodSetInfo
		var sparkPodSpec *sparkv1beta2.SparkPodSpec
		var emptyPodTemplate *corev1.PodTemplateSpec
		var changed bool

		switch role {
		case sparkcommon.SparkRoleDriver:
			podSetInfo = podSetsInfo[0]
			sparkPodSpec = &j.Spec.Driver.SparkPodSpec
			emptyPodTemplate = emptyDriverPodTemplateSpec
		case sparkcommon.SparkRoleExecutor:
			podSetInfo = podSetsInfo[1]
			sparkPodSpec = &j.Spec.Executor.SparkPodSpec
			emptyPodTemplate = emptyExecutorPodTemplateSpec
		default:
			return false
		}

		if !maps.Equal(sparkPodSpec.Annotations, podSetInfo.Annotations) {
			sparkPodSpec.Annotations = maps.Clone(podSetInfo.Annotations)
			changed = true
		}
		if !maps.Equal(sparkPodSpec.Labels, podSetInfo.Labels) {
			sparkPodSpec.Labels = maps.Clone(podSetInfo.Labels)
			changed = true
		}
		if j.Spec.NodeSelector != nil {
			if !maps.Equal(j.Spec.NodeSelector, podSetInfo.NodeSelector) {
				j.Spec.NodeSelector = maps.Clone(podSetInfo.NodeSelector)
				changed = true
			}
		} else {
			if !maps.Equal(sparkPodSpec.NodeSelector, podSetInfo.NodeSelector) {
				sparkPodSpec.NodeSelector = maps.Clone(podSetInfo.NodeSelector)
				changed = true
			}
		}
		if !slices.Equal(sparkPodSpec.Tolerations, podSetInfo.Tolerations) {
			sparkPodSpec.Tolerations = slices.Clone(podSetInfo.Tolerations)
			changed = true
		}
		if sparkPodSpec.Template == nil {
			sparkPodSpec.Template = emptyPodTemplate.DeepCopy()
		}
		if !slices.Equal(sparkPodSpec.Template.Spec.SchedulingGates, podSetInfo.SchedulingGates) {
			sparkPodSpec.Template.Spec.SchedulingGates = slices.Clone(podSetInfo.SchedulingGates)
			changed = true
		}

		if role == sparkcommon.SparkRoleExecutor {
			j.Spec.Executor.Instances = ptr.To(podSetInfo.Count)
		}

		return changed
	}

	driverChanged := restorePodSetsInfoFrom(sparkcommon.SparkRoleDriver)
	executorChanged := restorePodSetsInfoFrom(sparkcommon.SparkRoleExecutor)

	return driverChanged || executorChanged
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
