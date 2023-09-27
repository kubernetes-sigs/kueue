/*
Copyright 2022 The Kubernetes Authors.

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

package job

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
)

var (
	parentWorkloadKey = ".metadata.parentWorkload"
	gvk               = batchv1.SchemeGroupVersion.WithKind("Job")

	FrameworkName = "batch/job"
)

const (
	JobMinParallelismAnnotation              = "kueue.x-k8s.io/job-min-parallelism"
	JobCompletionsEqualParallelismAnnotation = "kueue.x-k8s.io/job-completions-equal-parallelism"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupWebhook,
		JobType:                &batchv1.Job{},
		IsManagingObjectsOwner: isJob,
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get;update
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(
	func() jobframework.GenericJob {
		return &Job{}
	}, func(c client.Client) handler.EventHandler {
		return &parentWorkloadHandler{client: c}
	},
)

func isJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "Job" && owner.APIVersion == gvk.GroupVersion().String()
}

type parentWorkloadHandler struct {
	client client.Client
}

func (h *parentWorkloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildJob(ctx, e.Object, q)
}

func (h *parentWorkloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildJob(ctx, e.ObjectNew, q)
}

func (h *parentWorkloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *parentWorkloadHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

// queueReconcileForChildJob queues reconciliation of the child jobs (jobs with the
// parent-workload annotation) in reaction to the parent-workload events.
func (h *parentWorkloadHandler) queueReconcileForChildJob(ctx context.Context, object client.Object, q workqueue.RateLimitingInterface) {
	w, ok := object.(*kueue.Workload)
	if !ok {
		return
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(w))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Queueing reconcile for child jobs")
	var childJobs batchv1.JobList
	if err := h.client.List(ctx, &childJobs, client.InNamespace(w.Namespace), client.MatchingFields{parentWorkloadKey: w.Name}); err != nil {
		klog.Error(err, "Unable to list child jobs")
		return
	}
	for _, childJob := range childJobs.Items {
		log.V(5).Info("Queueing reconcile for child job", "job", klog.KObj(&childJob))
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      childJob.Name,
				Namespace: w.Namespace,
			},
		})
	}
}

type Job batchv1.Job

var _ jobframework.GenericJob = (*Job)(nil)
var _ jobframework.JobWithReclaimablePods = (*Job)(nil)
var _ jobframework.JobWithCustomStop = (*Job)(nil)

func (j *Job) Object() client.Object {
	return (*batchv1.Job)(j)
}

func fromObject(o runtime.Object) *Job {
	return (*Job)(o.(*batchv1.Job))
}

func (j *Job) IsSuspended() bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

func (j *Job) IsActive() bool {
	return j.Status.Active != 0
}

func (j *Job) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *Job) Stop(ctx context.Context, c client.Client, podSetsInfo []jobframework.PodSetInfo, eventMsg string) (bool, error) {
	stoppedNow := false
	if !j.IsSuspended() {
		j.Suspend()
		if err := c.Update(ctx, j.Object()); err != nil {
			return false, fmt.Errorf("suspend: %w", err)
		}
		stoppedNow = true
	}

	// Reset start time, if necessary so we can update the scheduling directives.
	if j.Status.StartTime != nil {
		j.Status.StartTime = nil
		if err := c.Status().Update(ctx, j.Object()); err != nil {
			return stoppedNow, fmt.Errorf("reset status: %w", err)
		}
	}

	if changed := j.RestorePodSetsInfo(podSetsInfo); !changed {
		return stoppedNow, nil
	}
	if err := c.Update(ctx, j.Object()); err != nil {
		return false, fmt.Errorf("restore info: %w", err)
	}
	return stoppedNow, nil
}

func (j *Job) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *Job) ReclaimablePods() []kueue.ReclaimablePod {
	parallelism := ptr.Deref(j.Spec.Parallelism, 1)
	if parallelism == 1 || j.Status.Succeeded == 0 {
		return nil
	}

	remaining := ptr.Deref(j.Spec.Completions, parallelism) - j.Status.Succeeded
	if remaining >= parallelism {
		return nil
	}

	return []kueue.ReclaimablePod{{
		Name:  kueue.DefaultPodSetName,
		Count: parallelism - remaining,
	}}
}

func (j *Job) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Name:     kueue.DefaultPodSetName,
			Template: j.Spec.Template.DeepCopy(),
			Count:    j.podsCount(),
			MinCount: j.minPodsCount(),
		},
	}
}

func (j *Job) RunWithPodSetsInfo(podSetsInfo []jobframework.PodSetInfo) error {
	j.Spec.Suspend = ptr.To(false)
	if len(podSetsInfo) != 1 {
		return jobframework.BadPodSetsInfoLenError(1, len(podSetsInfo))
	}

	info := podSetsInfo[0]
	j.Spec.Template.Spec.NodeSelector = utilmaps.MergeKeepFirst(info.NodeSelector, j.Spec.Template.Spec.NodeSelector)

	if j.minPodsCount() != nil {
		j.Spec.Parallelism = ptr.To(info.Count)
		if j.syncCompletionWithParallelism() {
			j.Spec.Completions = j.Spec.Parallelism
		}
	}
	return nil
}

func (j *Job) RestorePodSetsInfo(podSetsInfo []jobframework.PodSetInfo) bool {
	if len(podSetsInfo) == 0 {
		return false
	}

	changed := false
	// if the job accepts partial admission
	if j.minPodsCount() != nil && ptr.Deref(j.Spec.Parallelism, 0) != podSetsInfo[0].Count {
		changed = true
		j.Spec.Parallelism = ptr.To(podSetsInfo[0].Count)
		if j.syncCompletionWithParallelism() {
			j.Spec.Completions = j.Spec.Parallelism
		}
	}

	if equality.Semantic.DeepEqual(j.Spec.Template.Spec.NodeSelector, podSetsInfo[0].NodeSelector) {
		return changed
	}
	j.Spec.Template.Spec.NodeSelector = maps.Clone(podSetsInfo[0].NodeSelector)
	return true
}

func (j *Job) Finished() (metav1.Condition, bool) {
	var conditionType batchv1.JobConditionType
	var finished bool

	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}

	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: "Job finished successfully",
	}
	if conditionType == batchv1.JobFailed {
		condition.Message = "Job failed"
	}

	return condition, finished
}

func (j *Job) PodsReady() bool {
	ready := ptr.Deref(j.Status.Ready, 0)
	return j.Status.Succeeded+ready >= j.podsCount()
}

func (j *Job) podsCount() int32 {
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	podsCount := *(j.Spec.Parallelism)
	if j.Spec.Completions != nil && *j.Spec.Completions < podsCount {
		podsCount = *j.Spec.Completions
	}
	return podsCount
}

func (j *Job) minPodsCount() *int32 {
	if strVal, found := j.GetAnnotations()[JobMinParallelismAnnotation]; found {
		if iVal, err := strconv.Atoi(strVal); err == nil {
			return ptr.To[int32](int32(iVal))
		}
	}
	return nil
}

func (j *Job) syncCompletionWithParallelism() bool {
	if strVal, found := j.GetAnnotations()[JobCompletionsEqualParallelismAnnotation]; found {
		if bVal, err := strconv.ParseBool(strVal); err == nil {
			return bVal
		}
	}
	return false
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &batchv1.Job{}, parentWorkloadKey, func(o client.Object) []string {
		job := fromObject(o)
		if pwName := jobframework.ParentWorkloadName(job); pwName != "" {
			return []string{pwName}
		}
		return nil
	}); err != nil {
		return err
	}
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
