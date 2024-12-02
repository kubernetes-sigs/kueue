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
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

var (
	gvk = batchv1.SchemeGroupVersion.WithKind("Job")

	FrameworkName = "batch/job"
)

const (
	JobMinParallelismAnnotation              = "kueue.x-k8s.io/job-min-parallelism"
	JobCompletionsEqualParallelismAnnotation = "kueue.x-k8s.io/job-completions-equal-parallelism"
	StoppingAnnotation                       = "kueue.x-k8s.io/stopping"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:           SetupIndexes,
		NewJob:                 NewJob,
		NewReconciler:          NewReconciler,
		SetupWebhook:           SetupWebhook,
		JobType:                &batchv1.Job{},
		IsManagingObjectsOwner: isJob,
		MultiKueueAdapter:      &multikueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch

func NewJob() jobframework.GenericJob {
	return &Job{}
}

var NewReconciler = jobframework.NewGenericReconcilerFactory(NewJob, func(b *builder.Builder, c client.Client) *builder.Builder {
	return b.Watches(&kueue.Workload{}, &parentWorkloadHandler{client: c})
})

func isJob(owner *metav1.OwnerReference) bool {
	return owner.Kind == "Job" && owner.APIVersion == gvk.GroupVersion().String()
}

type parentWorkloadHandler struct {
	client client.Client
}

func (h *parentWorkloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForChildJob(ctx, e.Object, q)
}

func (h *parentWorkloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.queueReconcileForChildJob(ctx, e.ObjectNew, q)
}

func (h *parentWorkloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *parentWorkloadHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// queueReconcileForChildJob queues reconciliation of the child jobs (jobs with the
// parent-workload annotation) in reaction to the parent-workload events.
func (h *parentWorkloadHandler) queueReconcileForChildJob(ctx context.Context, object client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	w, ok := object.(*kueue.Workload)
	if !ok {
		return
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(w))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Queueing reconcile for child jobs")
	var childJobs batchv1.JobList
	owner := metav1.GetControllerOf(w)
	if owner == nil {
		return
	}
	if err := h.client.List(ctx, &childJobs, client.InNamespace(w.Namespace), client.MatchingFields{indexer.OwnerReferenceUID: string(owner.UID)}); err != nil {
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
	return j.Spec.Suspend != nil && *j.Spec.Suspend && j.Annotations[StoppingAnnotation] != "true"
}

func (j *Job) IsActive() bool {
	return j.Status.Active != 0
}

func (j *Job) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *Job) Stop(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, _ jobframework.StopReason, _ string) (bool, error) {
	object := j.Object()
	stoppedNow := false

	if !j.IsSuspended() {
		if err := clientutil.Patch(ctx, c, object, true, func() (bool, error) {
			j.Suspend()
			if j.ObjectMeta.Annotations == nil {
				j.ObjectMeta.Annotations = map[string]string{}
			}
			// We are using annotation to be sure that all updates finished successfully.
			j.ObjectMeta.Annotations[StoppingAnnotation] = "true"
			return true, nil
		}); err != nil {
			return false, fmt.Errorf("suspend: %w", err)
		}
		stoppedNow = true
	}

	// Reset start time if necessary, so we can update the scheduling directives.
	if j.Status.StartTime != nil {
		if err := clientutil.PatchStatus(ctx, c, object, func() (bool, error) {
			j.Status.StartTime = nil
			return true, nil
		}); err != nil {
			return stoppedNow, fmt.Errorf("reset status: %w", err)
		}
	}

	if err := clientutil.Patch(ctx, c, object, true, func() (bool, error) {
		j.RestorePodSetsInfo(podSetsInfo)
		delete(j.ObjectMeta.Annotations, StoppingAnnotation)
		return true, nil
	}); err != nil {
		return false, fmt.Errorf("restore info: %w", err)
	}

	return stoppedNow, nil
}

func (j *Job) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *Job) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", batchv1.JobNameLabel, j.Name)
}

func (j *Job) ReclaimablePods() ([]kueue.ReclaimablePod, error) {
	parallelism := ptr.Deref(j.Spec.Parallelism, 1)
	if parallelism == 1 || j.Status.Succeeded == 0 {
		return nil, nil
	}

	remaining := ptr.Deref(j.Spec.Completions, parallelism) - j.Status.Succeeded
	if remaining >= parallelism {
		return nil, nil
	}

	return []kueue.ReclaimablePod{{
		Name:  kueue.DefaultPodSetName,
		Count: parallelism - remaining,
	}}, nil
}

// The following labels are managed internally by batch/job controller, we should not
// propagate them to the workload.
var (
	// the legacy names are no longer defined in the api, only in k/2/apis/batch
	legacyJobNameLabel       = "job-name"
	legacyControllerUIDLabel = "controller-uid"
	ManagedLabels            = []string{legacyJobNameLabel, legacyControllerUIDLabel, batchv1.JobNameLabel, batchv1.ControllerUidLabel}
)

func cleanManagedLabels(pt *corev1.PodTemplateSpec) *corev1.PodTemplateSpec {
	for _, managedLabel := range ManagedLabels {
		delete(pt.Labels, managedLabel)
	}
	return pt
}

func (j *Job) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Name:     kueue.DefaultPodSetName,
			Template: *cleanManagedLabels(j.Spec.Template.DeepCopy()),
			Count:    j.podsCount(),
			MinCount: j.minPodsCount(),
			TopologyRequest: jobframework.PodSetTopologyRequest(&j.Spec.Template.ObjectMeta,
				ptr.To(batchv1.JobCompletionIndexAnnotation), nil, nil),
		},
	}
}

func (j *Job) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	j.Spec.Suspend = ptr.To(false)
	if len(podSetsInfo) != 1 {
		return podset.BadPodSetsInfoLenError(1, len(podSetsInfo))
	}

	info := podSetsInfo[0]

	if j.minPodsCount() != nil {
		j.Spec.Parallelism = ptr.To(info.Count)
		if j.syncCompletionWithParallelism() {
			j.Spec.Completions = j.Spec.Parallelism
		}
	}
	return podset.Merge(&j.Spec.Template.ObjectMeta, &j.Spec.Template.Spec, info)
}

func (j *Job) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
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
	info := podSetsInfo[0]
	for _, managedLabel := range ManagedLabels {
		if v, found := j.Spec.Template.Labels[managedLabel]; found {
			info.AddOrUpdateLabel(managedLabel, v)
		}
	}
	changed = podset.RestorePodSpec(&j.Spec.Template.ObjectMeta, &j.Spec.Template.Spec, info) || changed
	return changed
}

func (j *Job) Finished() (message string, success, finished bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Message, c.Type != batchv1.JobFailed, true
		}
	}

	return "", true, false
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

func SetupIndexes(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	if err := fieldIndexer.IndexField(ctx, &batchv1.Job{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID); err != nil {
		return err
	}
	return jobframework.SetupWorkloadOwnerIndex(ctx, fieldIndexer, gvk)
}

func GetWorkloadNameForJob(jobName string, jobUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, jobUID, gvk)
}
