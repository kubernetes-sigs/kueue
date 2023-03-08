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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
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
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
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
	var job batchv1.Job
	if err := r.client.Get(ctx, req.NamespacedName, &job); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("job", klog.KObj(&job))
	ctx = ctrl.LoggerInto(ctx, log)

	isStandaloneJob := parentWorkloadName(&job) == ""

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have either
	// queue-name or the parent-workload annotation set.
	if !r.manageJobsWithoutQueueName && queueName(&job) == "" && isStandaloneJob {
		log.V(3).Info(fmt.Sprintf("Neither %s, nor %s annotation is set, ignoring the job", constants.QueueAnnotation, constants.ParentWorkloadAnnotation))
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling Job")

	// 1. make sure there is only a single existing instance of the workload.
	// If there's no workload exists and job is unsuspended, we'll stop it immediately.
	wl, err := r.ensureAtMostOneWorkload(ctx, &job)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if jobFinishedCond, jobFinished := jobFinishedCondition(&job); jobFinished {
		if !isStandaloneJob || wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}
		condition := generateFinishedCondition(jobFinishedCond)
		apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
		err := r.client.Status().Update(ctx, wl)
		if err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, err
	}

	// 3. handle workload is nil.
	if wl == nil {
		if !isStandaloneJob {
			return ctrl.Result{}, nil
		}
		err := r.handleJobWithNoWorkload(ctx, &job)
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
			condition := generatePodsReadyCondition(&job, wl)
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
	if jobSuspended(&job) {
		// start the job if the workload has been admitted, and the job is still suspended
		if wl.Status.Admission != nil {
			log.V(2).Info("Job admitted, unsuspending")
			err := r.startJob(ctx, wl, &job)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// update queue name if changed.
		q := queueName(&job)
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
		err := r.stopJob(ctx, wl, &job, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

// podsReady checks if all pods are ready or succeeded
func podsReady(job *batchv1.Job) bool {
	ready := pointer.Int32Deref(job.Status.Ready, 0)
	return job.Status.Succeeded+ready >= podsCount(&job.Spec)
}

// stopJob sends updates to suspend the job, reset the startTime so we can update the scheduling directives
// later when unsuspending and resets the nodeSelector to its previous state based on what is available in
// the workload (which should include the original affinities that the job had).
func (r *JobReconciler) stopJob(ctx context.Context, w *kueue.Workload,
	job *batchv1.Job, eventMsg string) error {
	job.Spec.Suspend = pointer.Bool(true)
	if err := r.client.Update(ctx, job); err != nil {
		return err
	}
	r.record.Eventf(job, corev1.EventTypeNormal, "Stopped", eventMsg)

	// Reset start time so we can update the scheduling directives later when unsuspending.
	if job.Status.StartTime != nil {
		job.Status.StartTime = nil
		if err := r.client.Status().Update(ctx, job); err != nil {
			return err
		}
	}

	if w != nil && !equality.Semantic.DeepEqual(job.Spec.Template.Spec.NodeSelector,
		w.Spec.PodSets[0].Template.Spec.NodeSelector) {
		job.Spec.Template.Spec.NodeSelector = map[string]string{}
		for k, v := range w.Spec.PodSets[0].Template.Spec.NodeSelector {
			job.Spec.Template.Spec.NodeSelector[k] = v
		}
		return r.client.Update(ctx, job)
	}

	return nil
}

func (r *JobReconciler) startJob(ctx context.Context, w *kueue.Workload, job *batchv1.Job) error {
	log := ctrl.LoggerFrom(ctx)

	if len(w.Spec.PodSets) != 1 {
		return fmt.Errorf("one podset must exist, found %d", len(w.Spec.PodSets))
	}
	nodeSelector, err := r.getNodeSelectors(ctx, w)
	if err != nil {
		return err
	}
	if len(nodeSelector) != 0 {
		if job.Spec.Template.Spec.NodeSelector == nil {
			job.Spec.Template.Spec.NodeSelector = nodeSelector
		} else {
			for k, v := range nodeSelector {
				job.Spec.Template.Spec.NodeSelector[k] = v
			}
		}
	} else {
		log.V(3).Info("no nodeSelectors to inject")
	}

	job.Spec.Suspend = pointer.Bool(false)
	if err := r.client.Update(ctx, job); err != nil {
		return err
	}

	r.record.Eventf(job, corev1.EventTypeNormal, "Started",
		"Admitted by clusterQueue %v", w.Status.Admission.ClusterQueue)
	return nil
}

func (r *JobReconciler) getNodeSelectors(ctx context.Context, w *kueue.Workload) (map[string]string, error) {
	if len(w.Status.Admission.PodSetFlavors[0].Flavors) == 0 {
		return nil, nil
	}

	processedFlvs := sets.New[kueue.ResourceFlavorReference]()
	nodeSelector := map[string]string{}
	for _, flvName := range w.Status.Admission.PodSetFlavors[0].Flavors {
		if processedFlvs.Has(flvName) {
			continue
		}
		// Lookup the ResourceFlavors to fetch the node affinity labels to apply on the job.
		flv := kueue.ResourceFlavor{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(flvName)}, &flv); err != nil {
			return nil, err
		}
		for k, v := range flv.Spec.NodeLabels {
			nodeSelector[k] = v
		}
		processedFlvs.Insert(flvName)
	}
	return nodeSelector, nil
}

func (r *JobReconciler) handleJobWithNoWorkload(ctx context.Context, job *batchv1.Job) error {
	log := ctrl.LoggerFrom(ctx)

	// Wait until there are no active pods.
	if job.Status.Active != 0 {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	// Create the corresponding workload.
	wl, err := ConstructWorkloadFor(ctx, r.client, job, r.scheme)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}

	r.record.Eventf(job, corev1.EventTypeNormal, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	return nil
}

// ensureAtMostOneWorkload finds a matching workload and deletes redundant ones.
func (r *JobReconciler) ensureAtMostOneWorkload(ctx context.Context, job *batchv1.Job) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload

	if pwName := parentWorkloadName(job); pwName != "" {
		pw := kueue.Workload{}
		NamespacedName := types.NamespacedName{
			Name:      pwName,
			Namespace: job.Namespace,
		}
		if err := r.client.Get(ctx, NamespacedName, &pw); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			log.V(2).Info("job with no matching parent workload", "parent-workload", pwName)
		} else {
			match = &pw
		}
	}

	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads, client.InNamespace(job.Namespace),
		client.MatchingFields{ownerKey: job.Name}); err != nil {
		log.Error(err, "Unable to list child workloads")
		return nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		owner := metav1.GetControllerOf(w)
		// Indexes don't work in unit tests, so we explicitly check for the
		// owner here.
		if owner.Name != job.Name {
			continue
		}
		if match == nil && jobAndWorkloadEqual(job, w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	// If there is no matching workload and the job is running, suspend it.
	if match == nil && !jobSuspended(job) {
		log.V(2).Info("job with no matching workload, suspending")
		var w *kueue.Workload
		if len(workloads.Items) == 1 {
			// The job may have been modified and hence the existing workload
			// doesn't match the job anymore. All bets are off if there are more
			// than one workload...
			w = &workloads.Items[0]
		}
		if err := r.stopJob(ctx, w, job, "No matching Workload"); err != nil {
			log.Error(err, "stopping job")
		}
	}

	// Delete duplicate workload instances.
	existedWls := 0
	for i := range toDelete {
		err := r.client.Delete(ctx, toDelete[i])
		if err == nil || !apierrors.IsNotFound(err) {
			existedWls++
		}
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete workload")
		}
		if err == nil {
			r.record.Eventf(job, corev1.EventTypeNormal, "DeletedWorkload",
				"Deleted not matching Workload: %v", workload.Key(toDelete[i]))
		}
	}

	if existedWls != 0 {
		if match == nil {
			return nil, fmt.Errorf("no matching workload was found, tried deleting %d existing workload(s)", existedWls)
		}
		return nil, fmt.Errorf("only one workload should exist, found %d", len(workloads.Items))
	}

	return match, nil
}

func ConstructWorkloadFor(ctx context.Context, client client.Client,
	job *batchv1.Job, scheme *runtime.Scheme) (*kueue.Workload, error) {
	w := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetWorkloadNameForJob(job.Name),
			Namespace: job.Namespace,
		},
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{
				{
					Template: *job.Spec.Template.DeepCopy(),
					Count:    podsCount(&job.Spec),
				},
			},
			QueueName: queueName(job),
		},
	}

	// Populate priority from priority class.
	priorityClassName, p, err := utilpriority.GetPriorityFromPriorityClass(
		ctx, client, job.Spec.Template.Spec.PriorityClassName)
	if err != nil {
		return nil, err
	}
	w.Spec.Priority = &p
	w.Spec.PriorityClassName = priorityClassName

	if err := ctrl.SetControllerReference(job, w, scheme); err != nil {
		return nil, err
	}

	return w, nil
}

func podsCount(jobSpec *batchv1.JobSpec) int32 {
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	podsCount := *(jobSpec.Parallelism)
	if jobSpec.Completions != nil && *jobSpec.Completions < podsCount {
		podsCount = *jobSpec.Completions
	}
	return podsCount
}

func generatePodsReadyCondition(job *batchv1.Job, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the the condition when the pods transition
	// Ready to Completed. As pods finish, they transition first into the
	// uncountedTerminatedPods staging area, before passing to the
	// succeeded/failed counters.
	if wl.Status.Admission != nil && (podsReady(job) || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
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

func generateFinishedCondition(jobStatus batchv1.JobConditionType) metav1.Condition {
	message := "Job finished successfully"
	if jobStatus == batchv1.JobFailed {
		message = "Job failed"
	}
	return metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: message,
	}
}

// From https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/utils.go
func jobFinishedCondition(j *batchv1.Job) (batchv1.JobConditionType, bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Type, true
		}
	}
	return "", false
}

func jobSuspended(j *batchv1.Job) bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}

func jobAndWorkloadEqual(job *batchv1.Job, wl *kueue.Workload) bool {
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
