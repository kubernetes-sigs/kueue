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
	"errors"
	"fmt"
	"strconv"

	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	kJobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

var (
	gvk                    = kftrainerapi.GroupVersion.WithKind("TrainJob")
	FrameworkName          = "trainer.kubeflow.org/trainjob"
	TrainJobControllerName = "trainer.kubeflow.org/trainjob-controller"
)

const (
	FirstOverrideIdx = "kueue.x-k8s.io/override-idx"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupTrainJobWebhook,
		JobType:           &kftrainerapi.TrainJob{},
		AddToScheme:       kftrainerapi.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs/finalizers,verbs=get;update

type trainJobReconciler struct {
	jr     *jobframework.JobReconciler
	client client.Client
}

var reconciler trainJobReconciler
var _ jobframework.JobReconcilerInterface = (*trainJobReconciler)(nil)

func NewReconciler(client client.Client, eventRecorder record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	reconciler = trainJobReconciler{
		jr:     jobframework.NewReconciler(client, eventRecorder, opts...),
		client: client,
	}
	return &reconciler
}

func (r *trainJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	jobSet := jobsetapi.JobSet{}
	err := r.client.Get(ctx, req.NamespacedName, &jobSet)
	if err != nil {
		// we only reconcile the TrainJob when its child JobSet is available
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.jr.ReconcileGenericJob(ctx, req, &TrainJob{})
}

func (r *trainJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kftrainerapi.TrainJob{}).Owns(&kueue.Workload{}).Owns(&jobsetapi.JobSet{})
	return b.Complete(r)
}

type TrainJob kftrainerapi.TrainJob

var _ jobframework.GenericJob = (*TrainJob)(nil)
var _ jobframework.JobWithCustomStop = (*TrainJob)(nil)
var _ jobframework.JobWithReclaimablePods = (*TrainJob)(nil)
var _ jobframework.JobWithManagedBy = (*TrainJob)(nil)

func NewJob() jobframework.GenericJob {
	return &TrainJob{}
}

func fromObject(obj runtime.Object) *TrainJob {
	return (*TrainJob)(obj.(*kftrainerapi.TrainJob))
}

func (t *TrainJob) Object() client.Object {
	return (*kftrainerapi.TrainJob)(t)
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

func getChildJobSet(t *TrainJob) (*jobsetapi.JobSet, error) {
	jobSet := jobsetapi.JobSet{}
	// TODO: Ideally we should be using the parent context here
	err := reconciler.client.Get(context.Background(), types.NamespacedName{Name: t.Name, Namespace: t.Namespace}, &jobSet)
	return &jobSet, err
}

func (t *TrainJob) PodSets() ([]kueue.PodSet, error) {
	jobset, err := getChildJobSet(t)
	if err != nil {
		return nil, err
	}
	return (*kJobset.JobSet)(jobset).PodSets()
}

func (t *TrainJob) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	jobset, err := getChildJobSet(t)
	if err != nil {
		return err
	}

	if len(podSetsInfo) != len(jobset.Spec.ReplicatedJobs) {
		return podset.BadPodSetsInfoLenError(len(jobset.Spec.ReplicatedJobs), len(podSetsInfo))
	}

	if t.Spec.PodSpecOverrides == nil {
		t.Spec.PodSpecOverrides = []kftrainerapi.PodSpecOverride{}
	}
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[FirstOverrideIdx] = strconv.Itoa(len(t.Spec.PodSpecOverrides))
	for _, info := range podSetsInfo {
		// The trainjob controller merges each podSpecOverride sequentially, so any existing user provided override will be processed first
		t.Spec.PodSpecOverrides = append(t.Spec.PodSpecOverrides, kftrainerapi.PodSpecOverride{
			TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
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
	// TODO: Ideally we should be using the parent context here
	err = reconciler.client.Update(context.Background(), t.Object())
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
		delete(t.Annotations, FirstOverrideIdx)
		return t.Object(), true, nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (t *TrainJob) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	idx, ok := t.Annotations[FirstOverrideIdx]
	if !ok {
		// kueue didn't inject any config yet
		return true
	}
	idxInt, err := strconv.Atoi(idx)
	if err != nil {
		return false
	}
	t.Spec.PodSpecOverrides = t.Spec.PodSpecOverrides[:idxInt]
	return true
}

func (t *TrainJob) Finished() (message string, success, finished bool) {
	if c := apimeta.FindStatusCondition(t.Status.Conditions, kftrainerapi.TrainJobComplete); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, true, true
	}
	if c := apimeta.FindStatusCondition(t.Status.Conditions, kftrainerapi.TrainJobFailed); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message, false, true
	}
	return message, success, false
}

func (t *TrainJob) PodsReady() bool {
	jobset, err := getChildJobSet(t)
	if err != nil {
		return false
	}
	return (*kJobset.JobSet)(jobset).PodsReady()
}

func (t *TrainJob) ReclaimablePods() ([]kueue.ReclaimablePod, error) {
	jobset, err := getChildJobSet(t)
	if err != nil {
		return nil, err
	}
	return (*kJobset.JobSet)(jobset).ReclaimablePods()
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
