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

package trainjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftrainerruntime "github.com/kubeflow/trainer/v2/pkg/runtime"
	kftrainerruntimecore "github.com/kubeflow/trainer/v2/pkg/runtime/core"
	kftrainerjobset "github.com/kubeflow/trainer/v2/pkg/runtime/framework/plugins/jobset"
	trainjobutil "github.com/kubeflow/trainer/v2/pkg/util/trainjob"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	jobsetapplyapi "sigs.k8s.io/jobset/client-go/applyconfiguration/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

var (
	gvk                    = kftrainer.GroupVersion.WithKind("TrainJob")
	FrameworkName          = "trainer.kubeflow.org/trainjob"
	TrainJobControllerName = "trainer.kubeflow.org/trainjob-controller"
)

const (
	// This is alpha level annotation
	firstOverrideIdx = "kueue.x-k8s.io/trainjob-override-idx"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupTrainJobWebhook,
		JobType:           &kftrainer.TrainJob{},
		AddToScheme:       kftrainer.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainingruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=clustertrainingruntimes,verbs=get;list;watch

type trainJobReconciler struct {
	jr               *jobframework.JobReconciler
	runtimes         map[string]kftrainerruntime.Runtime
	client           client.Client
	runtimeInfoMutex sync.Mutex
}

var reconciler trainJobReconciler
var _ jobframework.JobReconcilerInterface = (*trainJobReconciler)(nil)

func NewReconciler(ctx context.Context, client client.Client, indexer client.FieldIndexer, eventRecorder record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	runtimes, err := kftrainerruntimecore.New(ctx, client, indexer)
	if err != nil {
		return nil, err
	}
	reconciler = trainJobReconciler{
		jr:       jobframework.NewReconciler(client, eventRecorder, opts...),
		client:   client,
		runtimes: runtimes,
	}
	return &reconciler, nil
}

func (r *trainJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jr.ReconcileGenericJob(ctx, req, &TrainJob{})
}

func (r *trainJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kftrainer.TrainJob{}).Owns(&kueue.Workload{}).Owns(&jobsetapi.JobSet{})
	return b.Complete(r)
}

type TrainJob kftrainer.TrainJob

var _ jobframework.GenericJob = (*TrainJob)(nil)
var _ jobframework.JobWithCustomStop = (*TrainJob)(nil)
var _ jobframework.JobWithReclaimablePods = (*TrainJob)(nil)
var _ jobframework.JobWithManagedBy = (*TrainJob)(nil)

func NewJob() jobframework.GenericJob {
	return &TrainJob{}
}

func fromObject(obj runtime.Object) *TrainJob {
	return (*TrainJob)(obj.(*kftrainer.TrainJob))
}

func (t *TrainJob) Object() client.Object {
	return (*kftrainer.TrainJob)(t)
}

func (t *TrainJob) IsSuspended() bool {
	return ptr.Deref(t.Spec.Suspend, false)
}

func (t *TrainJob) IsActive() bool {
	for i := range t.Status.JobsStatus {
		if t.Status.JobsStatus[i].Active > 0 {
			return true
		}
	}
	return false
}

func (t *TrainJob) Suspend() {
	t.Spec.Suspend = ptr.To(true)
}

func (t *TrainJob) Unsuspend() {
	t.Spec.Suspend = ptr.To(false)
}

func (t *TrainJob) GVK() schema.GroupVersionKind {
	return gvk
}

func (t *TrainJob) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", jobsetapi.JobSetNameKey, t.Name)
}

func getChildJobSet(ctx context.Context, t *TrainJob) (*jobsetapi.JobSet, error) {
	runtimeRefGK := kftrainerruntime.RuntimeRefToRuntimeRegistryKey(t.Spec.RuntimeRef)
	runtime, ok := reconciler.runtimes[runtimeRefGK]
	if !ok {
		return nil, fmt.Errorf("unsupported runtime: %s", runtimeRefGK)
	}

	trainJob := (*kftrainer.TrainJob)(t)
	trSpec, err := getRuntimeSpec(ctx, trainJob)
	if err != nil {
		return nil, fmt.Errorf("runtime '%s' not found", trainJob.Spec.RuntimeRef.Name)
	}
	info, err := reconciler.runtimeInfo(runtime, trainJob, trSpec)
	if err != nil {
		return nil, err
	}

	// Get the jobsetSpecApply and apply the TrainJob object overrides for the trainer and initializer jobs
	jobSetSpec, ok := kftrainerruntime.TemplateSpecApply[jobsetapplyapi.JobSetSpecApplyConfiguration](info)
	if !ok {
		return nil, err
	}
	jobsetApply := kftrainerjobset.NewBuilder(jobsetapplyapi.JobSet(t.Name, t.Namespace).
		WithSpec(jobSetSpec)).Initializer(trainJob).Trainer(info, trainJob).PodLabels(info.Scheduler.PodLabels).Build()

	// convert to jobset with the defaults set
	return jobsetApplyToJobset(jobsetApply)
}

// TODO: Remove mutex once RuntimeInfo gets thread safe - https://github.com/kubeflow/trainer/issues/2873
func (r *trainJobReconciler) runtimeInfo(runtime kftrainerruntime.Runtime, trainJob *kftrainer.TrainJob, trSpec *kftrainer.TrainingRuntimeSpec) (*kftrainerruntime.Info, error) {
	r.runtimeInfoMutex.Lock()
	defer r.runtimeInfoMutex.Unlock()
	return runtime.RuntimeInfo(trainJob, trSpec.Template, trSpec.MLPolicy, trSpec.PodGroupPolicy)
}

func jobsetApplyToJobset(jobsetApply *jobsetapplyapi.JobSetApplyConfiguration) (*jobsetapi.JobSet, error) {
	jsonData, err := json.Marshal(jobsetApply)
	if err != nil {
		return nil, err
	}

	jobset := &jobsetapi.JobSet{}
	if err := json.Unmarshal(jsonData, jobset); err != nil {
		return nil, err
	}

	return jobset, nil
}

func getRuntimeSpec(ctx context.Context, trainJob *kftrainer.TrainJob) (*kftrainer.TrainingRuntimeSpec, error) {
	if trainjobutil.RuntimeRefIsClusterTrainingRuntime(trainJob.Spec.RuntimeRef) {
		var ctr kftrainer.ClusterTrainingRuntime
		err := reconciler.client.Get(ctx, client.ObjectKey{Name: trainJob.Spec.RuntimeRef.Name}, &ctr)
		if err != nil {
			return nil, err
		}
		return &ctr.Spec, nil
	} else {
		var tr kftrainer.TrainingRuntime
		err := reconciler.client.Get(ctx, client.ObjectKey{Namespace: trainJob.Namespace, Name: trainJob.Spec.RuntimeRef.Name}, &tr)
		if err != nil {
			return nil, err
		}
		return &tr.Spec, nil
	}
}

func (t *TrainJob) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	jobset, err := getChildJobSet(ctx, t)
	if err != nil {
		return nil, err
	}

	podsets, err := (*workloadjobset.JobSet)(jobset).PodSets(ctx)
	if err != nil {
		return nil, err
	}

	// Run a dry-run patch of a throwaway workload to set the podset defaults
	// Podsets must be defaulted because Kueue later uses them to match workloads.
	// Workloads coming from the API server are already defaulted, so without defaulting these podsets, matching would fail.
	wl := jobframework.NewWorkload(t.Name, t.Object(), podsets, []string{})
	if err := reconciler.client.Create(ctx, wl, &client.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		return nil, err
	}

	return wl.Spec.PodSets, nil
}

func (t *TrainJob) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
	jobset, err := getChildJobSet(ctx, t)
	if err != nil {
		return err
	}

	if len(podSetsInfo) != len(jobset.Spec.ReplicatedJobs) {
		return podset.BadPodSetsInfoLenError(len(jobset.Spec.ReplicatedJobs), len(podSetsInfo))
	}

	if t.Spec.PodSpecOverrides == nil {
		t.Spec.PodSpecOverrides = []kftrainer.PodSpecOverride{}
	}
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[firstOverrideIdx] = strconv.Itoa(len(t.Spec.PodSpecOverrides))
	for _, info := range podSetsInfo {
		// The trainjob controller merges each podSpecOverride sequentially, so any existing user provided override will be processed first
		t.Spec.PodSpecOverrides = append(t.Spec.PodSpecOverrides, kftrainer.PodSpecOverride{
			TargetJobs: []kftrainer.PodSpecOverrideTargetJob{
				{Name: string(info.Name)},
			},
			// TODO: Set the labels/annotations when supported. See https://github.com/kubeflow/trainer/pull/2785
			//
			// NOTE: Due to the issue above, in TAS mode, missing PodSet-specific labels and annotations
			//       prevent removal of the scheduling gate, leaving the Pod in a Pending state.
			NodeSelector:    info.NodeSelector,
			Tolerations:     info.Tolerations,
			SchedulingGates: info.SchedulingGates,
		})
	}
	// Update the podSpecOverrides while the job is suspended, since is a requirement from the trainjob admission webhook
	err = reconciler.client.Update(ctx, t.Object())
	if err != nil {
		return err
	}

	t.Unsuspend()
	return nil
}

func (t *TrainJob) Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, _ jobframework.StopReason, _ string) (bool, error) {
	if !t.IsSuspended() {
		t.Suspend()
		if err := reconciler.client.Update(ctx, t.Object()); err != nil {
			return false, fmt.Errorf("error suspending trainjob: %w", err)
		}
	}

	if t.IsActive() {
		return false, errors.New("jobs are still active")
	}

	if err := clientutil.Patch(ctx, reconciler.client, t.Object(), func() (client.Object, bool, error) {
		if !t.RestorePodSetsInfo(podSetsInfo) {
			return t.Object(), false, errors.New("error restoring info to the trainjob")
		}
		delete(t.Annotations, firstOverrideIdx)
		return t.Object(), true, nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (t *TrainJob) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	idx, ok := t.Annotations[firstOverrideIdx]
	if !ok {
		// Kueue didn't inject any config yet
		return true
	}
	idxInt, err := strconv.Atoi(idx)
	if err != nil {
		return false
	}
	t.Spec.PodSpecOverrides = t.Spec.PodSpecOverrides[:idxInt]
	return true
}

func (t *TrainJob) Finished(ctx context.Context) (message string, success, finished bool) {
	if c := apimeta.FindStatusCondition(t.Status.Conditions, kftrainer.TrainJobComplete); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, true, true
	}
	if c := apimeta.FindStatusCondition(t.Status.Conditions, kftrainer.TrainJobFailed); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, false, true
	}
	return message, success, false
}

func (t *TrainJob) PodsReady(ctx context.Context) bool {
	jobset, err := getChildJobSet(ctx, t)
	if err != nil {
		return false
	}

	var replicas int32
	for _, replicatedJob := range jobset.Spec.ReplicatedJobs {
		replicas += replicatedJob.Replicas
	}
	var readyReplicas int32
	for _, jobStatus := range t.Status.JobsStatus {
		readyReplicas += jobStatus.Ready + jobStatus.Succeeded
	}
	return replicas == readyReplicas
}

func (t *TrainJob) ReclaimablePods(ctx context.Context) ([]kueue.ReclaimablePod, error) {
	if len(t.Status.JobsStatus) == 0 {
		return nil, nil
	}
	jobset, err := getChildJobSet(ctx, t)
	if err != nil {
		return nil, err
	}

	ret := make([]kueue.ReclaimablePod, 0, len(jobset.Spec.ReplicatedJobs))
	statuses := slices.ToRefMap(t.Status.JobsStatus, func(js *kftrainer.JobStatus) string { return js.Name })

	for i := range jobset.Spec.ReplicatedJobs {
		spec := &jobset.Spec.ReplicatedJobs[i]
		if status, found := statuses[spec.Name]; found && status.Succeeded > 0 {
			if status.Succeeded > 0 && status.Succeeded <= spec.Replicas {
				ret = append(ret, kueue.ReclaimablePod{
					Name:  kueue.NewPodSetReference(spec.Name),
					Count: status.Succeeded * workloadjobset.PodsCountPerReplica(spec),
				})
			}
		}
	}
	return ret, nil
}

func (t *TrainJob) CanDefaultManagedBy() bool {
	trainJobSpecManagedBy := t.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(trainJobSpecManagedBy == nil || *trainJobSpecManagedBy == TrainJobControllerName)
}

func (t *TrainJob) ManagedBy() *string {
	return t.Spec.ManagedBy
}

func (t *TrainJob) SetManagedBy(managedBy *string) {
	t.Spec.ManagedBy = managedBy
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForTrainJob(trainJobName string, trainJobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(trainJobName, trainJobUID, gvk)
}
