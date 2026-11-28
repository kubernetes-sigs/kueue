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
	"strings"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobframeworktas "sigs.k8s.io/kueue/pkg/controller/jobframework/tas"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	gvk = sparkv1beta2.GroupVersion.WithKind("SparkApplication")
)

const (
	FrameworkName      = "sparkoperator.k8s.io/sparkapplication"
	driverPodSetName   = "driver"
	executorPodSetName = "executor"

	// maxExecutorIDAnnotation records the highest executor ID of the current
	// submission, which feeds the elastic workload-slice name.
	maxExecutorIDAnnotation = "kueue.x-k8s.io/spark-max-executor-id"
	// maxExecutorIDSubmissionAnnotation records the submission that
	// maxExecutorIDAnnotation belongs to.
	maxExecutorIDSubmissionAnnotation = "kueue.x-k8s.io/spark-max-executor-id-submission"
	// failedExecutorIDsAnnotation records the failed executor IDs of the current
	// submission, so they do not count towards the created executors while the
	// driver ramps up to its starting executors.
	failedExecutorIDsAnnotation = "kueue.x-k8s.io/spark-failed-executor-ids"
	// executorsStartedSubmissionAnnotation records the submission whose executors
	// have started: its starting executors (see startingExecutors) have all been
	// ready.
	executorsStartedSubmissionAnnotation = "kueue.x-k8s.io/spark-executors-started-submission"
)

// The driver and executor PodSets are always present, in this order.
const (
	driverPodSetIndex   = 0
	executorPodSetIndex = 1
	podSetsCount        = 2
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
// +kubebuilder:rbac:groups=sparkoperator.k8s.io,resources=sparkapplications/finalizers,verbs=get;update

func NewJob() jobframework.GenericJob {
	return &SparkApplication{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob,
	func(b *builder.Builder, _ client.Client) *builder.Builder {
		// PodsReady and the executor count of an elastic job are taken from the executor pods,
		// as the Spark Operator does not report every executor in the SparkApplication status.
		return b.Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(executorPodToSparkApplication),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: executorPodChanged}))
	},
)

func executorPodToSparkApplication(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	appName := labels[sparkcommon.LabelSparkAppName]
	if labels[sparkcommon.LabelSparkRole] != sparkcommon.SparkRoleExecutor || appName == "" {
		return nil
	}
	return []reconcile.Request{{Namespace: obj.GetNamespace(), Name: appName}}
}

// executorPodChanged reports whether an executor pod changed what is counted from it: its phase,
// its readiness, or whether it is being deleted.
func executorPodChanged(e event.UpdateEvent) bool {
	oldPod, isOldPod := e.ObjectOld.(*corev1.Pod)
	newPod, isNewPod := e.ObjectNew.(*corev1.Pod)
	if !isOldPod || !isNewPod {
		return true
	}
	return oldPod.Status.Phase != newPod.Status.Phase ||
		podReady(oldPod) != podReady(newPod) ||
		(oldPod.DeletionTimestamp == nil) != (newPod.DeletionTimestamp == nil)
}

type SparkApplication sparkv1beta2.SparkApplication

var _ jobframework.GenericJob = (*SparkApplication)(nil)
var _ jobframework.ElasticWorkloadNameProvider = (*SparkApplication)(nil)
var _ jobframework.JobWithCustomAnnotations = (*SparkApplication)(nil)
var _ jobframework.JobWithPartialPreemption = (*SparkApplication)(nil)

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

func (j *SparkApplication) PodSets(ctx context.Context, c client.Client) ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, podSetsCount)

	setTopologyRequestToPodSetIfEnabled := func(podSet *kueue.PodSet, podTemplateSpec *corev1.PodTemplateSpec) error {
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframeworktas.NewPodSetTopologyRequest(
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
	podSets[driverPodSetIndex] = kueue.PodSet{
		Name:     driverPodSetName,
		Template: *driverPodTemplateSpec,
		Count:    1,
	}

	if err := setTopologyRequestToPodSetIfEnabled(
		&podSets[driverPodSetIndex], driverPodTemplateSpec,
	); err != nil {
		return nil, err
	}

	// executors
	executorPodTemplateSpec, err := j.buildExecutorPodTemplateSpec()
	if err != nil {
		return nil, err
	}
	executorCount, err := j.numExecutors(ctx, c)
	if err != nil {
		return nil, err
	}
	podSets[executorPodSetIndex] = kueue.PodSet{
		Name:     executorPodSetName,
		Template: *executorPodTemplateSpec,
		Count:    executorCount,
	}
	// The first slice is admitted all-or-nothing, and the scale-up slices keep
	// this floor. Kueue lowers it when the executors scale down below it.
	if da := j.dynamicAllocation(); jobframework.ElasticPartialScaleUpEnabled(j) && da.Enabled {
		podSets[executorPodSetIndex].MinCount = new(executorCount)
	}
	if minCount := podSetPreemptionMinRetainedCount(executorPodTemplateSpec); minCount != nil {
		podSets[executorPodSetIndex].PartialPreemption = &kueue.PodSetPartialPreemption{MinRetainedCount: minCount}
	}

	if err := setTopologyRequestToPodSetIfEnabled(
		&podSets[executorPodSetIndex], executorPodTemplateSpec,
	); err != nil {
		return nil, err
	}

	return podSets, nil
}

func (j *SparkApplication) RunWithPodSetsInfo(ctx context.Context, _ client.Client, podSetsInfo []podset.PodSetInfo) error {
	if len(podSetsInfo) != podSetsCount {
		return podset.BadPodSetsInfoLenError(podSetsCount, len(podSetsInfo))
	}

	j.Spec.Suspend = new(false)

	origGlobalNodeSelector := maps.Clone(j.Spec.NodeSelector)

	mutatePodSetInfoFor := func(role string) error {
		var podSetInfo podset.PodSetInfo
		var nodeSelector map[string]string
		var sparkPodSpec *sparkv1beta2.SparkPodSpec

		switch role {
		case sparkcommon.SparkRoleDriver:
			podSetInfo = podSetsInfo[driverPodSetIndex]
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
			podSetInfo = podSetsInfo[executorPodSetIndex]
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

		// podset.Merge replaces stale Kueue-managed annotations (workload, workload-slice-name)
		// left by a previous admission of an elastic job, instead of reporting a conflict.
		meta := metav1.ObjectMeta{
			Annotations: sparkPodSpec.Annotations,
			Labels:      sparkPodSpec.Labels,
		}
		spec := corev1.PodSpec{
			NodeSelector:    nodeSelector,
			Tolerations:     sparkPodSpec.Tolerations,
			SchedulingGates: sparkPodSpec.Template.Spec.SchedulingGates,
		}
		if err := podset.Merge(ctrl.LoggerFrom(ctx), &meta, &spec, podSetInfo); err != nil {
			return err
		}
		sparkPodSpec.Annotations = meta.Annotations
		sparkPodSpec.Labels = meta.Labels
		sparkPodSpec.NodeSelector = spec.NodeSelector
		sparkPodSpec.Tolerations = spec.Tolerations
		sparkPodSpec.Template.Spec.SchedulingGates = spec.SchedulingGates
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
	if len(podSetsInfo) != podSetsCount {
		ctrl.LoggerFrom(ctx).V(1).Info(
			"Skipping pod set info restore because the pod set count does not match the admitted workload",
			"expectedCount", podSetsCount,
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
			podSetInfo = podSetsInfo[driverPodSetIndex]
			sparkPodSpec = &j.Spec.Driver.SparkPodSpec
			emptyPodTemplate = emptyDriverPodTemplateSpec
		case sparkcommon.SparkRoleExecutor:
			podSetInfo = podSetsInfo[executorPodSetIndex]
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
		// For any other job, restoring the count can only undo an edit of
		// spec.executor.instances, so it is restored only when that field is set: a
		// count set in spec.sparkConf stays there, and an edit of it takes effect.
		if role == sparkcommon.SparkRoleExecutor && !isAnElasticJob(j) &&
			j.Spec.Executor.Instances != nil && *j.Spec.Executor.Instances != podSetInfo.Count {
			// An unset spec.executor.instances is counted as 0 executors, while the
			// CRD requires the field to be at least 1 when set. Restore 0 as unset,
			// otherwise the API server rejects the patch that suspends the job.
			if podSetInfo.Count == 0 {
				j.Spec.Executor.Instances = nil
			} else {
				j.Spec.Executor.Instances = new(podSetInfo.Count)
			}
			changed = true
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

func (j *SparkApplication) PodsReady(ctx context.Context, c client.Client, options ...jobframework.PodsReadyOption) bool {
	// Driver must be running.
	if j.Status.AppState.State != sparkv1beta2.ApplicationStateRunning {
		return false
	}
	// And enough executors must be ready: running and ready, and not being deleted.
	// AppState.State alone goes to Running as soon as the driver starts even if
	// executors are stuck (e.g. unschedulable), which would let the
	// waitForPodsReady timeout never fire on heterogeneous resource shortages.
	wl := jobframework.PodsReadyWorkload(options...)
	expected := int(j.expectedExecutors(wl))
	// Partial preemption can shrink the executors below that.
	if wl != nil && isAnElasticJob(j) && features.Enabled(features.PartialPreemption) {
		if retained, ok := workload.PreemptionRetainedCount(wl, executorPodSetName); ok {
			expected = min(expected, int(retained))
		}
	}
	if expected == 0 {
		return true
	}
	ready, err := j.readyExecutors(ctx, c)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to count the ready executors")
		return false
	}
	return ready >= expected
}

// readyExecutors counts the executor pods of the current submission that are running and ready,
// and not being deleted. It reads the pods rather than status.executorState, which the Spark
// Operator stops updating for executors whose ID passes its tracking limit, and whose COMPLETED
// entries are executors the application no longer has.
func (j *SparkApplication) readyExecutors(ctx context.Context, c client.Client) (int, error) {
	pods, err := j.listExecutorPods(ctx, c)
	if err != nil {
		return 0, err
	}
	var ready int
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && podReady(pod) {
			ready++
		}
	}
	return ready, nil
}

func podReady(pod *corev1.Pod) bool {
	return utilpod.HasCondition(pod, &corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
}

// expectedExecutors returns the executors that must be ready for PodsReady in
// the workload wl. Without dynamic allocation, it's the executor instances.
func (j *SparkApplication) expectedExecutors(wl *kueue.Workload) int32 {
	da := j.dynamicAllocation()
	if !da.Enabled {
		return j.numInitialExecutors()
	}
	// A job that is not elastic is sized at its executor instances, and its
	// executors fluctuate down to minExecutors, which defaults to the instances.
	if !isAnElasticJob(j) {
		return ptr.Deref(da.MinExecutors, j.numInitialExecutors())
	}
	// Once the executors of this submission have started (see
	// GetCustomAnnotations), Spark can scale down to minExecutors, 0 if unset.
	if j.executorsStarted() {
		return ptr.Deref(da.MinExecutors, 0)
	}
	// Before then, the executors the driver starts with, capped at the grant: a
	// lower target shrinks the workload. A first workload created while the
	// driver already runs can be sized above them.
	expected := j.startingExecutors(da)
	if wl != nil && workload.IsAdmitted(wl) {
		if granted, ok := workload.ExtractGrantedPodSetCounts(wl)[executorPodSetName]; ok {
			expected = min(expected, granted)
		}
	}
	return expected
}

// executorsStarted reports whether the executors of the current submission have
// started: executorsStartedSubmissionAnnotation records it. Recording it per
// submission, rather than reading the workload's PodsReady condition, holds a
// resubmission's driver on the executors it starts with, whether it keeps the
// workload slice or gets a new one.
func (j *SparkApplication) executorsStarted() bool {
	return j.Status.SubmissionID != "" && j.Annotations[executorsStartedSubmissionAnnotation] == j.Status.SubmissionID
}

// startingExecutors returns the executors the driver starts with under dynamic
// allocation: its initial target, capped at maxExecutors.
func (j *SparkApplication) startingExecutors(da sparkv1beta2.DynamicAllocation) int32 {
	initialTarget := j.initialExecutorTarget(da)
	return min(initialTarget, ptr.Deref(da.MaxExecutors, initialTarget))
}

// initialExecutorTarget returns the executor target the driver starts with
// under dynamic allocation da: the largest of instances, minExecutors and
// initialExecutors. Spark does not cap it at maxExecutors.
func (j *SparkApplication) initialExecutorTarget(da sparkv1beta2.DynamicAllocation) int32 {
	return max(j.numInitialExecutors(), ptr.Deref(da.MinExecutors, 0), ptr.Deref(da.InitialExecutors, 0))
}

// numExecutors returns the executor count to request quota for. With dynamic
// allocation, an elastic job follows the executors the driver keeps (pending or
// running), so terminating executors hold quota until they stop. Without a
// client (the webhook), the count follows the spec only.
func (j *SparkApplication) numExecutors(ctx context.Context, c client.Client) (int32, error) {
	da := j.dynamicAllocation()
	if !isAnElasticJob(j) || !da.Enabled {
		return j.numInitialExecutors(), nil
	}
	initialTarget := j.initialExecutorTarget(da)
	if c == nil || !j.driverStarted() {
		return initialTarget, nil
	}
	count, maxID, failedIDs, err := j.countExecutorPods(ctx, c)
	if err != nil {
		return 0, err
	}
	// The driver creates its initial executors in batches, so fewer executors before
	// it has created them all are not a scale-down. Spark numbers the executors of a
	// submission from 1 and never reuses an ID, so the highest executor ID is the
	// number of executors the driver has created. The driver can lower its target
	// before it creates them all, and it stops an executor only after lowering its
	// target, so once one has stopped the driver is no longer ramping up. The pods
	// of stopped executors can be deleted, so the highest ID also comes from
	// maxExecutorIDAnnotation. While the driver ramps up, Spark replaces a failed
	// executor with a new, higher ID, so failed executors do not count as created.
	// Once the starting executors have started, the ramp-up is over and failures
	// no longer matter here.
	created := max(j.recordedMaxExecutorID(), maxID)
	if !j.executorsStarted() {
		created = max(0, created-len(failedIDs))
	}
	createdAll := created >= int(j.startingExecutors(da))
	if !createdAll && created <= int(count) {
		return initialTarget, nil
	}
	// Clamp the count to [minExecutors, maxExecutors]. minExecutors goes last, so
	// the count never drops below the executor PodSet's minCount.
	if maxExecutors := da.MaxExecutors; maxExecutors != nil {
		count = min(count, *maxExecutors)
	}
	if minExecutors := da.MinExecutors; minExecutors != nil {
		count = max(count, *minExecutors)
	}
	return count, nil
}

// countExecutorPods returns the number of executor pods of the current
// submission that the driver keeps, pending or running, and the highest
// executor ID among all of them. It reads pods rather than
// status.executorState, because the Spark Operator stops adding executors
// there once their ID exceeds its tracking limit.
func (j *SparkApplication) countExecutorPods(ctx context.Context, c client.Client) (int32, int, sets.Set[int], error) {
	pods, err := j.listExecutorPods(ctx, c)
	if err != nil {
		return 0, 0, nil, err
	}
	var active int32
	var maxID int
	failedIDs := j.recordedFailedExecutorIDs()
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodRunning {
			active++
		}
		if id, err := strconv.Atoi(pod.Labels[sparkcommon.LabelSparkExecutorID]); err == nil {
			maxID = max(maxID, id)
			if pod.Status.Phase == corev1.PodFailed {
				failedIDs.Insert(id)
			}
		}
	}
	return active, maxID, failedIDs, nil
}

// recordedMaxExecutorID returns the highest executor ID recorded in
// maxExecutorIDAnnotation for the current submission, or 0 if the record
// belongs to a previous one.
func (j *SparkApplication) recordedMaxExecutorID() int {
	if j.Annotations[maxExecutorIDSubmissionAnnotation] != j.Status.SubmissionID {
		return 0
	}
	id, _ := strconv.Atoi(j.Annotations[maxExecutorIDAnnotation])
	return id
}

// recordedFailedExecutorIDs returns the failed executor IDs recorded in
// failedExecutorIDsAnnotation for the current submission, or an empty set if the
// record belongs to a previous one.
func (j *SparkApplication) recordedFailedExecutorIDs() sets.Set[int] {
	ids := sets.New[int]()
	if j.Annotations[maxExecutorIDSubmissionAnnotation] != j.Status.SubmissionID {
		return ids
	}
	for _, v := range strings.Split(j.Annotations[failedExecutorIDsAnnotation], ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ids.Insert(id)
		}
	}
	return ids
}

func formatFailedExecutorIDs(ids sets.Set[int]) string {
	sorted := slices.Sorted(slices.Values(ids.UnsortedList()))
	strs := make([]string, len(sorted))
	for i, id := range sorted {
		strs[i] = strconv.Itoa(id)
	}
	return strings.Join(strs, ",")
}

// listExecutorPods lists the executor pods of the current submission.
func (j *SparkApplication) listExecutorPods(ctx context.Context, c client.Client) (*corev1.PodList, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(j.Namespace), client.MatchingLabels{
		sparkcommon.LabelSparkAppName: j.Name,
		sparkcommon.LabelSparkRole:    sparkcommon.SparkRoleExecutor,
		sparkcommon.LabelSubmissionID: j.Status.SubmissionID,
	}, client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	return &pods, nil
}

// PodsToReclaim implements jobframework.JobWithPartialPreemption. It evicts executors only, never
// the driver, starting with the youngest. Terminating, terminated or gated
// executors are skipped.
func (j *SparkApplication) PodsToReclaim(ctx context.Context, c client.Client, _ *kueue.Workload, targets map[kueue.PodSetReference]int32) ([]*corev1.Pod, error) {
	target, ok := targets[executorPodSetName]
	if !ok {
		return nil, nil
	}
	pods, err := j.listExecutorPods(ctx, c)
	if err != nil {
		return nil, err
	}
	running := make([]*corev1.Pod, 0, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		if utilpod.IsTerminated(p) || p.DeletionTimestamp != nil || utilpod.HasGate(p, kueue.ElasticJobSchedulingGate) {
			continue
		}
		running = append(running, p)
	}
	if int32(len(running)) <= target {
		return nil, nil
	}
	slices.SortFunc(running, func(a, b *corev1.Pod) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	return running[target:], nil
}

// podSetPreemptionMinRetainedCount returns the partial-preemption floor read from the
// kueue.x-k8s.io/podset-partial-preemption-min-retained-count annotation of the pod template, or nil when
// the feature gate is off or the annotation is absent or malformed.
func podSetPreemptionMinRetainedCount(template *corev1.PodTemplateSpec) *int32 {
	if !features.Enabled(features.PartialPreemption) {
		return nil
	}
	strVal, found := template.Annotations[constants.PodSetPartialPreemptionMinRetainedCountAnnotation]
	if !found {
		return nil
	}
	minCount, err := strconv.ParseInt(strVal, 10, 32)
	if err != nil || minCount < 0 {
		return nil
	}
	return new(int32(minCount))
}

// driverStarted reports whether the driver of the current submission has started,
// so the executor count follows its executor pods. It includes the states after
// RUNNING: falling back to the initial executors there would look like a scale-up,
// and the job reconciler would then not find the active workload slice to finish it.
// It also includes UNKNOWN, which the Spark Operator sets while the driver pod phase
// is unknown and which is usually temporary.
func (j *SparkApplication) driverStarted() bool {
	switch j.Status.AppState.State {
	case sparkv1beta2.ApplicationStateRunning, sparkv1beta2.ApplicationStateUnknown,
		sparkv1beta2.ApplicationStateSucceeding, sparkv1beta2.ApplicationStateCompleted,
		sparkv1beta2.ApplicationStateFailing, sparkv1beta2.ApplicationStateFailed:
		return true
	}
	return false
}

// GetCustomAnnotations records the highest executor ID of the current
// submission of an elastic job in maxExecutorIDAnnotation, for numExecutors and
// GetWorkloadNameExtraPart. The recorded ID never decreases within a
// submission, so it survives the deletion of executor pods. With dynamic
// allocation, it also records in executorsStartedSubmissionAnnotation that the
// executors of the submission have started, once its starting executors are
// all ready, for PodsReady.
func (j *SparkApplication) GetCustomAnnotations(ctx context.Context, c client.Client) (map[string]string, error) {
	if !isAnElasticJob(j) || j.Status.SubmissionID == "" {
		return nil, nil
	}
	_, maxID, failedIDs, err := j.countExecutorPods(ctx, c)
	if err != nil {
		return nil, err
	}
	annotations := map[string]string{
		maxExecutorIDAnnotation:           strconv.Itoa(max(j.recordedMaxExecutorID(), maxID)),
		maxExecutorIDSubmissionAnnotation: j.Status.SubmissionID,
	}
	// The failed IDs only matter while the driver ramps up. Once its executors
	// have started they are no longer written, so the annotation stops growing;
	// the job reconciler only adds or updates annotations, so the recorded set
	// stays as it was.
	//
	// An existing record is rewritten even when the set is now empty, so the IDs
	// of a previous submission are cleared in the same update that moves
	// maxExecutorIDSubmissionAnnotation to the current one. Otherwise the next
	// reconcile would read them as failures of this submission.
	if !j.executorsStarted() && (len(failedIDs) > 0 || j.Annotations[failedExecutorIDsAnnotation] != "") {
		annotations[failedExecutorIDsAnnotation] = formatFailedExecutorIDs(failedIDs)
	}
	if da := j.dynamicAllocation(); da.Enabled && !j.executorsStarted() {
		ready, err := j.readyExecutors(ctx, c)
		if err != nil {
			return nil, err
		}
		// The driver can lower its target before it starts all its starting
		// executors, and the executor PodSet then shrinks with numExecutors, so
		// the starting executors are no more than numExecutors.
		target, err := j.numExecutors(ctx, c)
		if err != nil {
			return nil, err
		}
		if ready >= int(min(j.startingExecutors(da), target)) {
			annotations[executorsStartedSubmissionAnnotation] = j.Status.SubmissionID
		}
	}
	return annotations, nil
}

// GetWorkloadNameExtraPart folds the submission attempt and the highest
// executor ID into the elastic workload-slice name. The Spark driver scales
// executors without bumping metadata.generation; without this the slice name
// would key on that generation alone, so a second scale-up would recompute an
// already-existing slice name and fail to mint its replacement. Spark never
// reuses an executor ID within a submission, and a scale-up adds executors with
// new IDs, so the ID grows with every scale-up. status.executorState can't be
// used, as the Spark Operator stops adding executors there once their ID
// exceeds its tracking limit.
func (j *SparkApplication) GetWorkloadNameExtraPart() string {
	extra := strconv.FormatInt(j.Generation, 10)
	if j.Status.SubmissionAttempts > 0 {
		extra += "_" + strconv.Itoa(int(j.Status.SubmissionAttempts))
		maxExecutorID := j.Annotations[maxExecutorIDAnnotation]
		if maxExecutorID != "" {
			extra += "_" + maxExecutorID
		}
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
