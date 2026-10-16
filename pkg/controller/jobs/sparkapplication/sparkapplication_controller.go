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
	"strconv"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	gvk = sparkv1beta2.GroupVersion.WithKind("SparkApplication")
)

const (
	FrameworkName      = "sparkoperator.k8s.io/sparkapplication"
	driverPodSetName   = "driver"
	executorPodSetName = "executor"
)

func RegisterIntegration(m *jobframework.IntegrationManager) error {
	return m.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:          SetupIndexes,
		NewJob:                NewJob,
		NewReconciler:         NewReconciler,
		SetupWebhook:          SetupWebhook,
		JobType:               &sparkv1beta2.SparkApplication{},
		AddToScheme:           sparkv1beta2.AddToScheme,
		CanSupportIntegration: CanSupportIntegration,
	})
}

// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications,verbs=get;list;watch;update;patch;delete

func NewJob() jobframework.GenericJob {
	return &SparkApplication{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob)

type SparkApplication sparkv1beta2.SparkApplication

var _ jobframework.GenericJob = (*SparkApplication)(nil)
var _ jobframework.ElasticWorkloadNameProvider = (*SparkApplication)(nil)

func (j *SparkApplication) Object() client.Object {
	return (*sparkv1beta2.SparkApplication)(j)
}

func (j *SparkApplication) IsSuspended() bool {
	return ptr.Deref(j.Spec.Suspend, false)
}

func (j *SparkApplication) IsActive() bool {
	return j.Status.AppState.State == sparkv1beta2.ApplicationStateRunning
}

func (j *SparkApplication) Suspend() {
	j.Spec.Suspend = new(true)
}

func (j *SparkApplication) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *SparkApplication) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", sparkcommon.LabelSparkAppName, j.Name)
}

func (j *SparkApplication) PodSets(ctx context.Context, _ client.Client) ([]kueue.PodSet, error) {
	// driver and executor
	podSets := make([]kueue.PodSet, 2)

	setTopologyRequestToPodSetIfEnabled := func(podSet *kueue.PodSet, podTemplateSpec *corev1.PodTemplateSpec) error {
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(
				&podTemplateSpec.ObjectMeta).Build()
			if err != nil {
				return err
			}
			podSet.TopologyRequest = topologyRequest
		}
		return nil
	}

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

	if err := setTopologyRequestToPodSetIfEnabled(
		&podSets[0], driverPodTemplateSpec,
	); err != nil {
		return nil, err
	}

	// executors
	executorPodTemplateSpec, err := j.buildExecutorPodTemplateSpec()
	if err != nil {
		return nil, err
	}
	podSets[1] = kueue.PodSet{
		Name:     executorPodSetName,
		Template: *executorPodTemplateSpec,
		Count:    j.numExecutors(),
	}

	if err := setTopologyRequestToPodSetIfEnabled(
		&podSets[1], executorPodTemplateSpec,
	); err != nil {
		return nil, err
	}

	return podSets, nil
}

func (j *SparkApplication) RunWithPodSetsInfo(ctx context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := 2 // driver + executor
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.Suspend = new(false)

	origGlobalNodeSelector := maps.Clone(j.Spec.NodeSelector)

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
			if origGlobalNodeSelector != nil {
				nodeSelector = maps.Clone(origGlobalNodeSelector)
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
			if origGlobalNodeSelector != nil {
				nodeSelector = maps.Clone(origGlobalNodeSelector)
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
		sparkPodSpec.NodeSelector = sparkPodSetInfo.NodeSelector
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

	if origGlobalNodeSelector != nil {
		j.Spec.NodeSelector = nil
	}

	return nil
}

func (j *SparkApplication) RestorePodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) bool {
	expectedLength := 2 // driver + executor
	if len(podSetsInfo) != expectedLength {
		ctrl.LoggerFrom(ctx).V(2).Info(
			"Skipping pod set info restore because the pod set count does not match the admitted workload",
			"expectedCount", expectedLength,
			"gotCount", len(podSetsInfo),
		)
		return false
	}

	hadGlobalNodeSelector := j.Spec.NodeSelector != nil

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
		if !maps.Equal(sparkPodSpec.NodeSelector, podSetInfo.NodeSelector) {
			sparkPodSpec.NodeSelector = maps.Clone(podSetInfo.NodeSelector)
			changed = true
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

		// An elastic job's executor count follows the running executors, not
		// spec.executor.instances, and Kueue never changes instances when it
		// starts the job, so there is nothing to restore.
		if role == sparkcommon.SparkRoleExecutor && !isAnElasticJob((*sparkv1beta2.SparkApplication)(j)) {
			// An unset spec.executor.instances is counted as 0 executors, while the
			// CRD requires the field to be at least 1 when set. Restore 0 as unset,
			// otherwise the API server rejects the patch that suspends the job.
			if podSetInfo.Count == 0 {
				j.Spec.Executor.Instances = nil
			} else {
				j.Spec.Executor.Instances = new(podSetInfo.Count)
			}
		}

		return changed
	}

	driverChanged := restorePodSetsInfoFrom(sparkcommon.SparkRoleDriver)
	executorChanged := restorePodSetsInfoFrom(sparkcommon.SparkRoleExecutor)

	// Defensive: RunWithPodSetsInfo clears spec.nodeSelector, so this only
	// fires if the object was never admitted or was re-populated externally.
	if hadGlobalNodeSelector && j.Spec.NodeSelector != nil {
		j.Spec.NodeSelector = nil
		driverChanged = true
	}

	return driverChanged || executorChanged
}

func (j *SparkApplication) Finished(ctx context.Context) (message string, success, finished bool) {
	// SUBMISSION_FAILED is not terminal: depending on the restartPolicy, the
	// operator resubmits the application, and it moves the application to
	// FAILED once no retries are left.
	return j.Status.AppState.ErrorMessage,
		j.Status.AppState.State == sparkv1beta2.ApplicationStateCompleted,
		j.Status.AppState.State == sparkv1beta2.ApplicationStateCompleted ||
			j.Status.AppState.State == sparkv1beta2.ApplicationStateFailed
}

func (j *SparkApplication) PodsReady(ctx context.Context, _ client.Client) bool {
	// Driver must be running.
	if j.Status.AppState.State != sparkv1beta2.ApplicationStateRunning {
		return false
	}
	// And every requested executor must have reported a Running/Completed state.
	// AppState.State alone goes to Running as soon as the driver starts even if
	// executors are stuck (e.g. unschedulable), which would let the
	// waitForPodsReady timeout never fire on heterogeneous resource shortages.
	expected := int(ptr.Deref(j.Spec.Executor.Instances, 0))
	// When dynamic allocation is enabled, the actual number of executors can
	// fluctuate between minExecutors and maxExecutors. Use minExecutors as the
	// expected count since it's the guaranteed minimum. If neither the CRD
	// field nor sparkConf is set, use Spec.Executor.Instances as default.
	if j.Spec.DynamicAllocation != nil && j.Spec.DynamicAllocation.Enabled {
		expected = int(ptr.Deref(j.Spec.DynamicAllocation.MinExecutors, int32(expected)))
	}
	if expected == 0 {
		return true
	}
	ready := 0
	for _, st := range j.Status.ExecutorState {
		switch st {
		case sparkv1beta2.ExecutorStateRunning, sparkv1beta2.ExecutorStateCompleted:
			ready++
		}
	}
	return ready >= expected
}

// numExecutors returns the executor count to request quota for. With dynamic
// allocation, an elastic job follows the executors the driver keeps (pending or
// running), so terminating executors hold quota until they stop.
func (j *SparkApplication) numExecutors() int32 {
	if !workloadslicing.Enabled(j.Object()) || !ptr.Deref(j.Spec.DynamicAllocation, sparkv1beta2.DynamicAllocation{}).Enabled {
		return j.numInitialExecutors()
	}
	if !j.driverStarted() || len(j.Status.ExecutorState) == 0 {
		minExecutors := ptr.Deref(j.Spec.DynamicAllocation.MinExecutors, 0)
		return max(j.numInitialExecutors(), minExecutors)
	}
	var count int32
	for _, st := range j.Status.ExecutorState {
		if st == sparkv1beta2.ExecutorStatePending || st == sparkv1beta2.ExecutorStateRunning {
			count++
		}
	}
	// cap the upper and lower bounds
	if minExecutors := j.Spec.DynamicAllocation.MinExecutors; minExecutors != nil {
		count = max(count, *minExecutors)
	}
	if maxExecutors := j.Spec.DynamicAllocation.MaxExecutors; maxExecutors != nil {
		count = min(count, *maxExecutors)
	}
	return count
}

// driverStarted reports whether status.executorState belongs to the current
// submission. It includes the states after RUNNING: falling back to the initial
// executors there would look like a scale-up, and the job reconciler would then
// not find the active workload slice to finish it.
func (j *SparkApplication) driverStarted() bool {
	switch j.Status.AppState.State {
	case sparkv1beta2.ApplicationStateRunning,
		sparkv1beta2.ApplicationStateSucceeding, sparkv1beta2.ApplicationStateCompleted,
		sparkv1beta2.ApplicationStateFailing, sparkv1beta2.ApplicationStateFailed:
		return true
	}
	return false
}

// GetWorkloadNameExtraPart folds the submission attempt and the number of
// tracked executors into the elastic workload-slice name. The Spark driver
// scales executors without bumping metadata.generation; without this the slice
// name would key on that generation alone, so a second scale-up would recompute
// an already-existing slice name and fail to mint its replacement. The operator
// only adds entries to status.executorState within a submission attempt, so the
// count grows with every scale-up.
func (j *SparkApplication) GetWorkloadNameExtraPart() string {
	extra := strconv.FormatInt(j.Generation, 10)
	if j.Status.SubmissionAttempts > 0 {
		extra += fmt.Sprintf("_%d_%d", j.Status.SubmissionAttempts, len(j.Status.ExecutorState))
	}
	return extra
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForSparkApplication(sparkAppName string, sparkAppUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(sparkAppName, sparkAppUID, gvk)
}

func CanSupportIntegration(opts ...jobframework.Option) (bool, error) {
	if !features.Enabled(features.SparkApplicationIntegration) {
		return false, fmt.Errorf("%s integration is alpha feature. please enable %s featuregate", FrameworkName, features.SparkApplicationIntegration)
	}
	return true, nil
}

func fromObject(o runtime.Object) *SparkApplication {
	return (*SparkApplication)(o.(*sparkv1beta2.SparkApplication))
}
