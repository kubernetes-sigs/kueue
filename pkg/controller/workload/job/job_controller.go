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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ownerKey          = ".metadata.controller"
	parentWorkloadKey = ".metadata.parentWorkload"
)

// JobReconciler reconciles a Job object
type JobReconciler struct {
	client                     client.Client
	scheme                     *runtime.Scheme
	record                     record.EventRecorder
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
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

	return &JobReconciler{
		scheme:                     scheme,
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.manageJobsWithoutQueueName,
		waitForPodsReady:           options.waitForPodsReady,
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

//var _ GenericJob = &BatchJob{}

type BatchJob struct {
	batchv1.Job
}

func (b *BatchJob) Object() client.Object {
	return &b.Job
}

func (b *BatchJob) ParentWorkloadName() string {
	return parentWorkloadName(&b.Job)
}

func (b *BatchJob) QueueName() string {
	return queueName(&b.Job)
}

func (b *BatchJob) IsSuspend() bool {
	return b.Spec.Suspend != nil && *b.Spec.Suspend
}

func (b *BatchJob) IsActive() bool {
	return b.Status.Active != 0
}

func (b *BatchJob) Suspend() error {
	b.Spec.Suspend = pointer.Bool(true)
	return nil
}

func (b *BatchJob) UnSuspend() error {
	b.Spec.Suspend = pointer.Bool(false)
	return nil
}

func (b *BatchJob) ResetStatus() bool {
	// Reset start time so we can update the scheduling directives later when unsuspending.
	if b.Status.StartTime == nil {
		return false
	}
	b.Status.StartTime = nil
	return true
}

func (b *BatchJob) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Template: *b.Spec.Template.DeepCopy(),
			Count:    b.podsCount(),
		},
	}
}

func (b *BatchJob) InjectNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 {
		return nil
	}

	if b.Spec.Template.Spec.NodeSelector == nil {
		b.Spec.Template.Spec.NodeSelector = nodeSelectors[0]
	} else {
		for k, v := range nodeSelectors[0] {
			b.Spec.Template.Spec.NodeSelector[k] = v
		}
	}

	return nil
}

func (b *BatchJob) RestoreNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 || equality.Semantic.DeepEqual(b.Spec.Template.Spec.NodeSelector, nodeSelectors) {
		return nil
	}

	b.Spec.Template.Spec.NodeSelector = map[string]string{}

	for k, v := range nodeSelectors[0] {
		b.Spec.Template.Spec.NodeSelector[k] = v
	}
	return nil
}

func (b *BatchJob) Finished() (metav1.Condition, bool) {
	var conditionType batchv1.JobConditionType
	var finished bool

	for _, c := range b.Status.Conditions {
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

func (b *BatchJob) EquivalentToWorkload(wl kueue.Workload) bool {
	owner := metav1.GetControllerOf(&wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != b.Name {
		return false
	}

	if len(wl.Spec.PodSets) != 1 {
		return false
	}

	if *b.Spec.Parallelism != wl.Spec.PodSets[0].Count {
		return false
	}

	// nodeSelector may change, hence we are not checking for
	// equality of the whole job.Spec.Template.Spec.
	if !equality.Semantic.DeepEqual(b.Spec.Template.Spec.InitContainers,
		wl.Spec.PodSets[0].Template.Spec.InitContainers) {
		return false
	}
	return equality.Semantic.DeepEqual(b.Spec.Template.Spec.Containers,
		wl.Spec.PodSets[0].Template.Spec.Containers)
}

func (b *BatchJob) PriorityClass() string {
	return b.Spec.Template.Spec.PriorityClassName
}

func (b *BatchJob) PodsReady() bool {
	ready := pointer.Int32Deref(b.Job.Status.Ready, 0)
	return b.Job.Status.Succeeded+ready >= b.podsCount()
}

func (b *BatchJob) podsCount() int32 {
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	podsCount := *(b.Spec.Parallelism)
	if b.Spec.Completions != nil && *b.Spec.Completions < podsCount {
		podsCount = *b.Spec.Completions
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
		if pwName := parentWorkloadName(job); pwName != "" {
			return []string{pwName}
		}
		return nil
	}); err != nil {
		return err
	}
	return indexer.IndexField(ctx, &kueue.Workload{}, ownerKey, func(o client.Object) []string {
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
	var batchJob BatchJob
	if err := r.client.Get(ctx, req.NamespacedName, &batchJob.Job); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var genericJob GenericJob = &batchJob

	log := ctrl.LoggerFrom(ctx).WithValues("job", klog.KObj(&batchJob.Job))
	ctx = ctrl.LoggerInto(ctx, log)

	isStandaloneJob := genericJob.ParentWorkloadName() == ""

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have either
	// queue-name or the parent-workload annotation set.
	if !r.manageJobsWithoutQueueName && genericJob.QueueName() == "" && isStandaloneJob {
		log.V(3).Info(fmt.Sprintf("Neither %s, nor %s annotation is set, ignoring the job", constants.QueueAnnotation, constants.ParentWorkloadAnnotation))
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling Job")

	// 1. make sure there is only a single existing instance of the workload.
	// If there's no workload exists and job is unsuspended, we'll stop it immediately.
	wl, err := EnsureOneWorkload(ctx, r.client, req, r.record, genericJob)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if condition, finished := genericJob.Finished(); finished {
		if wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}
		if err := SetWorkloadCondition(ctx, r.client, wl, condition); err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, nil
	}

	// 3. handle workload is nil.
	if wl == nil {
		if !isStandaloneJob {
			return ctrl.Result{}, nil
		}
		err := r.handleJobWithNoWorkload(ctx, genericJob)
		if err != nil {
			log.Error(err, "Handling job with no workload")
		}
		return ctrl.Result{}, err
	}

	// 4. handle WaitForPodsReady only for a standalone job.
	if isStandaloneJob {
		// handle a job when waitForPodsReady is enabled, and it is the main job
		if r.waitForPodsReady {
			log.V(5).Info("Handling a job when waitForPodsReady is enabled")
			condition := generatePodsReadyCondition(genericJob, wl)
			// optimization to avoid sending the update request if the status didn't change
			if !apimeta.IsStatusConditionPresentAndEqual(wl.Status.Conditions, condition.Type, condition.Status) {
				log.V(3).Info(fmt.Sprintf("Updating the PodsReady condition with status: %v", condition.Status))
				apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
				if err := r.client.Status().Update(ctx, wl); err != nil {
					log.Error(err, "Updating workload status")
				}
			}
		}
	}

	// 5. handle job is suspended.
	if genericJob.IsSuspend() {
		// start the job if the workload has been admitted, and the job is still suspended
		if wl.Status.Admission != nil {
			log.V(2).Info("Job admitted, unsuspending")
			err := StartJob(ctx, r.client, r.record, genericJob, wl)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// update queue name if changed.
		q := genericJob.QueueName()
		if wl.Spec.QueueName != q {
			log.V(2).Info("Job changed queues, updating workload")
			wl.Spec.QueueName = q
			err := r.client.Update(ctx, wl)
			if err != nil {
				log.Error(err, "Updating workload queue")
			}
			return ctrl.Result{}, err
		}
		log.V(3).Info("Job is suspended and workload not yet admitted by a clusterQueue, nothing to do")
		return ctrl.Result{}, nil
	}

	// 6. handle job is unsuspended.
	if wl.Status.Admission == nil {
		// the job must be suspended if the workload is not yet admitted.
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		err := StopJob(ctx, r.client, r.record, genericJob, wl, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

func (r *JobReconciler) handleJobWithNoWorkload(ctx context.Context, genericJob GenericJob) error {
	log := ctrl.LoggerFrom(ctx)

	// Wait until there are no active pods.
	if genericJob.IsActive() {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	// Create the corresponding workload.
	wl, err := ConstructWorkload(ctx, r.client, r.scheme, genericJob)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}
	r.record.Eventf(genericJob.Object(), corev1.EventTypeNormal, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	return nil
}

func generatePodsReadyCondition(genericJob GenericJob, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the the condition when the pods transition
	// Ready to Completed. As pods finish, they transition first into the
	// uncountedTerminatedPods staging area, before passing to the
	// succeeded/failed counters.
	if wl.Status.Admission != nil && (genericJob.PodsReady() || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
		conditionStatus = metav1.ConditionTrue
		message = "All pods were ready or succeeded since the workload admission"
	}
	return metav1.Condition{
		Type:    kueue.WorkloadPodsReady,
		Status:  conditionStatus,
		Reason:  "PodsReady",
		Message: message,
	}
}

func queueName(job *batchv1.Job) string {
	if v, ok := job.Labels[constants.QueueLabel]; ok {
		return v
	}
	return job.Annotations[constants.QueueAnnotation]
}

func parentWorkloadName(job *batchv1.Job) string {
	return job.Annotations[constants.ParentWorkloadAnnotation]
}

func GetWorkloadNameForJob(jobName string) string {
	gvk := metav1.GroupVersionKind{Group: batchv1.SchemeGroupVersion.Group, Version: batchv1.SchemeGroupVersion.Version, Kind: "Job"}
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, &gvk)
}
