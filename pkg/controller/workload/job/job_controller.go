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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/jobframework"
)

var (
	parentWorkloadKey = ".metadata.parentWorkload"
	gvk               = batchv1.SchemeGroupVersion.WithKind("Job")
)

// JobReconciler reconciles a Job object
type JobReconciler struct {
	client        client.Client
	jobReconciler *jobframework.JobReconciler
}

type options struct {
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
}

// Option configures the reconciler.
type Option func(*options)

// WithManageJobsWithoutQueueName indicates if the controller should reconcile
// jobs that don't set the queue name annotation.
func WithManageJobsWithoutQueueName(f bool) Option {
	return func(o *options) {
		o.manageJobsWithoutQueueName = f
	}
}

// WithWaitForPodsReady indicates if the controller should add the PodsReady
// condition to the workload when the corresponding job has all pods ready
// or succeeded.
func WithWaitForPodsReady(f bool) Option {
	return func(o *options) {
		o.waitForPodsReady = f
	}
}

var defaultOptions = options{}

func NewReconciler(
	scheme *runtime.Scheme,
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *JobReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	jobReconciler := jobframework.NewReconciler(scheme,
		client,
		record,
		jobframework.WithWaitForPodsReady(options.waitForPodsReady),
		jobframework.WithManageJobsWithoutQueueName(options.manageJobsWithoutQueueName),
	)

	return &JobReconciler{
		client:        client,
		jobReconciler: jobReconciler,
	}
}

type parentWorkloadHandler struct {
	client client.Client
}

func (h *parentWorkloadHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildJob(e.Object, q)
}

func (h *parentWorkloadHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildJob(e.ObjectNew, q)
}

func (h *parentWorkloadHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *parentWorkloadHandler) Generic(e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

// queueReconcileForChildJob queues reconciliation of the child jobs (jobs with the
// parent-workload annotation) in reaction to the parent-workload events.
// TODO: replace the TODO context with the one passed to the event handler's functions
// when a new version of controller-runtime is used. See in master:
// https://github.com/kubernetes-sigs/controller-runtime/blob/master/pkg/handler/eventhandler.go
func (h *parentWorkloadHandler) queueReconcileForChildJob(object client.Object, q workqueue.RateLimitingInterface) {
	w, ok := object.(*kueue.Workload)
	if !ok {
		return
	}
	ctx := context.TODO()
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

type Job struct {
	batchv1.Job
}

func (job *Job) Object() client.Object {
	return &job.Job
}

func (job *Job) ParentWorkloadName() string {
	return job.Annotations[constants.ParentWorkloadAnnotation]
}

func (job *Job) QueueName() string {
	return job.Annotations[constants.QueueAnnotation]
}

func (job *Job) IsSuspend() bool {
	return job.Spec.Suspend != nil && *job.Spec.Suspend
}

func (job *Job) IsActive() bool {
	return job.Status.Active != 0
}

func (job *Job) Suspend() error {
	job.Spec.Suspend = pointer.Bool(true)
	return nil
}

func (job *Job) UnSuspend() error {
	job.Spec.Suspend = pointer.Bool(false)
	return nil
}

func (job *Job) ResetStatus() bool {
	// Reset start time so we can update the scheduling directives later when unsuspending.
	if job.Status.StartTime == nil {
		return false
	}
	job.Status.StartTime = nil
	return true
}

func (job *Job) GetGVK() schema.GroupVersionKind {
	return gvk
}

func (job *Job) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Template: *job.Spec.Template.DeepCopy(),
			Count:    job.podsCount(),
		},
	}
}

func (job *Job) InjectNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 {
		return nil
	}

	if job.Spec.Template.Spec.NodeSelector == nil {
		job.Spec.Template.Spec.NodeSelector = nodeSelectors[0]
	} else {
		for k, v := range nodeSelectors[0] {
			job.Spec.Template.Spec.NodeSelector[k] = v
		}
	}

	return nil
}

func (job *Job) RestoreNodeAffinity(podSets []kueue.PodSet) error {
	if len(podSets) == 0 || equality.Semantic.DeepEqual(job.Spec.Template.Spec.NodeSelector, podSets[0].Template.Spec.NodeSelector) {
		return nil
	}

	job.Spec.Template.Spec.NodeSelector = map[string]string{}

	for k, v := range podSets[0].Template.Spec.NodeSelector {
		job.Spec.Template.Spec.NodeSelector[k] = v
	}
	return nil
}

func (job *Job) Finished() (metav1.Condition, bool) {
	var conditionType batchv1.JobConditionType
	var finished bool

	for _, c := range job.Status.Conditions {
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

func (job *Job) EquivalentToWorkload(wl kueue.Workload) bool {
	if len(wl.Spec.PodSets) != 1 {
		return false
	}

	if *job.Spec.Parallelism != wl.Spec.PodSets[0].Count {
		return false
	}

	// nodeSelector may change, hence we are not checking for
	// equality of the whole job.Spec.Template.Spec.
	if !equality.Semantic.DeepEqual(job.Spec.Template.Spec.InitContainers,
		wl.Spec.PodSets[0].Template.Spec.InitContainers) {
		return false
	}
	return equality.Semantic.DeepEqual(job.Spec.Template.Spec.Containers,
		wl.Spec.PodSets[0].Template.Spec.Containers)
}

func (job *Job) PriorityClass() string {
	return job.Spec.Template.Spec.PriorityClassName
}

func (job *Job) PodsReady() bool {
	ready := pointer.Int32Deref(job.Status.Ready, 0)
	return job.Status.Succeeded+ready >= job.podsCount()
}

func (job *Job) podsCount() int32 {
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	podsCount := *(job.Spec.Parallelism)
	if job.Spec.Completions != nil && *job.Spec.Completions < podsCount {
		podsCount = *job.Spec.Completions
	}
	return podsCount
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	wlHandler := parentWorkloadHandler{client: r.client}
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Watches(&source.Kind{Type: &kueue.Workload{}}, &wlHandler).
		Owns(&kueue.Workload{}).
		Complete(r)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &batchv1.Job{}, parentWorkloadKey, func(o client.Object) []string {
		job := o.(*batchv1.Job)
		batchJob := Job{*job}
		if pwName := batchJob.ParentWorkloadName(); pwName != "" {
			return []string{pwName}
		}
		return nil
	}); err != nil {
		return err
	}
	return indexer.IndexField(ctx, &kueue.Workload{}, jobframework.GetOwnerKey(gvk), func(o client.Object) []string {
		// grab the Workload object, extract the owner...
		wl := o.(*kueue.Workload)
		owner := metav1.GetControllerOf(wl)
		// ...make sure it's a Job...
		if owner == nil || owner.APIVersion != batchv1.SchemeGroupVersion.String() || owner.Kind != "Job" {
			return nil
		}
		// ...and if so, return it
		return []string{owner.Name}
	})
}

//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
//+kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jobReconciler.ReconcileForJobObject(ctx, req, &Job{})
}

func GetWorkloadNameForJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, gvk)
}
