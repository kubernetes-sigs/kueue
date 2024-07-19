/*
Copyright 2023 The Kubernetes Authors.

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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/maps"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	FailedToStartFinishedReason = "FailedToStart"
)

var (
	ErrUnknownWorkloadOwner  = errors.New("workload owner is unknown")
	ErrWorkloadOwnerNotFound = errors.New("workload owner not found")
	ErrNoMatchingWorkloads   = errors.New("no matching workloads")
	ErrExtraWorkloads        = errors.New("extra workloads")
)

// JobReconciler reconciles a GenericJob object
type JobReconciler struct {
	client                     client.Client
	record                     record.EventRecorder
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
	labelKeysToCopy            []string
}

type Options struct {
	ManageJobsWithoutQueueName bool
	WaitForPodsReady           bool
	KubeServerVersion          *kubeversion.ServerVersionFetcher
	// IntegrationOptions key is "$GROUP/$VERSION, Kind=$KIND".
	IntegrationOptions        map[string]any
	EnabledFrameworks         sets.Set[string]
	EnabledExternalFrameworks sets.Set[string]
	ManagerName               string
	LabelKeysToCopy           []string
	Queues                    *queue.Manager
	Cache                     *cache.Cache
}

// Option configures the reconciler.
type Option func(*Options)

func ProcessOptions(opts ...Option) Options {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	return options
}

// WithManageJobsWithoutQueueName indicates if the controller should reconcile
// jobs that don't set the queue name annotation.
func WithManageJobsWithoutQueueName(f bool) Option {
	return func(o *Options) {
		o.ManageJobsWithoutQueueName = f
	}
}

// WithWaitForPodsReady indicates if the controller should add the PodsReady
// condition to the workload when the corresponding job has all pods ready
// or succeeded.
func WithWaitForPodsReady(w *configapi.WaitForPodsReady) Option {
	return func(o *Options) {
		o.WaitForPodsReady = w != nil && w.Enable
	}
}

func WithKubeServerVersion(v *kubeversion.ServerVersionFetcher) Option {
	return func(o *Options) {
		o.KubeServerVersion = v
	}
}

// WithIntegrationOptions adds integrations options like podOptions.
// The second arg, `opts` should be recognized as any option struct.
func WithIntegrationOptions(integrationName string, opts any) Option {
	return func(o *Options) {
		if len(o.IntegrationOptions) == 0 {
			o.IntegrationOptions = make(map[string]any)
		}
		o.IntegrationOptions[integrationName] = opts
	}
}

// WithEnabledFrameworks adds framework names enabled in the ConfigAPI.
func WithEnabledFrameworks(frameworks []string) Option {
	return func(o *Options) {
		if len(frameworks) == 0 {
			return
		}
		o.EnabledFrameworks = sets.New(frameworks...)
	}
}

// WithEnabledExternalFrameworks adds framework names managed by external controller in the Config API.
func WithEnabledExternalFrameworks(exFrameworks []string) Option {
	return func(o *Options) {
		if len(exFrameworks) == 0 {
			return
		}
		o.EnabledExternalFrameworks = sets.New(exFrameworks...)
	}
}

// WithManagerName adds the kueue's manager name.
func WithManagerName(n string) Option {
	return func(o *Options) {
		o.ManagerName = n
	}
}

// WithLabelKeysToCopy adds the label keys
func WithLabelKeysToCopy(n []string) Option {
	return func(o *Options) {
		o.LabelKeysToCopy = n
	}
}

// WithQueues adds the queue manager.
func WithQueues(q *queue.Manager) Option {
	return func(o *Options) {
		o.Queues = q
	}
}

// WithCache adds the cache manager.
func WithCache(c *cache.Cache) Option {
	return func(o *Options) {
		o.Cache = c
	}
}

var defaultOptions = Options{}

func NewReconciler(
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *JobReconciler {
	options := ProcessOptions(opts...)

	return &JobReconciler{
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		waitForPodsReady:           options.WaitForPodsReady,
		labelKeysToCopy:            options.LabelKeysToCopy,
	}
}

func (r *JobReconciler) ReconcileGenericJob(ctx context.Context, req ctrl.Request, job GenericJob) (result ctrl.Result, err error) {
	object := job.Object()
	log := ctrl.LoggerFrom(ctx).WithValues("job", req.String(), "gvk", job.GVK())
	ctx = ctrl.LoggerInto(ctx, log)

	defer func() {
		err = r.ignoreUnretryableError(log, err)
	}()

	dropFinalizers := false
	if cJob, isComposable := job.(ComposableJob); isComposable {
		dropFinalizers, err = cJob.Load(ctx, r.client, &req.NamespacedName)
	} else {
		err = r.client.Get(ctx, req.NamespacedName, object)
		dropFinalizers = apierrors.IsNotFound(err) || !object.GetDeletionTimestamp().IsZero()
	}

	if jws, implements := job.(JobWithSkip); implements {
		if jws.Skip() {
			return ctrl.Result{}, nil
		}
	}

	if dropFinalizers {
		// Remove workload finalizer
		workloads := &kueue.WorkloadList{}

		if cJob, isComposable := job.(ComposableJob); isComposable {
			var err error
			workloads, err = cJob.ListChildWorkloads(ctx, r.client, req.NamespacedName)
			if err != nil {
				log.Error(err, "Removing finalizer")
				return ctrl.Result{}, err
			}
		} else {
			if err := r.client.List(ctx, workloads, client.InNamespace(req.Namespace),
				client.MatchingFields{GetOwnerKey(job.GVK()): req.Name}); err != nil {
				log.Error(err, "Unable to list child workloads")
				return ctrl.Result{}, err
			}
		}
		for i := range workloads.Items {
			err := workload.RemoveFinalizer(ctx, r.client, &workloads.Items[i])
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "Removing finalizer")
				return ctrl.Result{}, err
			}
		}

		// Remove job finalizer
		if !object.GetDeletionTimestamp().IsZero() {
			if err = r.finalizeJob(ctx, job); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	isStandaloneJob := true
	objectOwner := metav1.GetControllerOf(object)
	if objectOwner != nil && IsOwnerManagedByKueue(objectOwner) {
		isStandaloneJob = false
	}

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have either
	// queue-name or the parent-workload annotation set.
	// If the parent-workload annotation is set, it also checks whether the parent job has queue-name label.
	if !r.manageJobsWithoutQueueName && QueueName(job) == "" {
		if isStandaloneJob {
			log.V(3).Info("queue-name label is not set, ignoring the job", "queueName", QueueName(job))
			return ctrl.Result{}, nil
		}
		isParentJobManaged, err := r.IsParentJobManaged(ctx, job.Object(), req.Namespace)
		if err != nil {
			log.Error(err, "couldn't check whether the parent job is managed by kueue")
			return ctrl.Result{}, err
		}
		if !isParentJobManaged {
			log.V(3).Info("parent-workload annotation is set, and the parent job doesn't have a queue-name label, ignoring the job",
				"parentJob", objectOwner.Name)
			return ctrl.Result{}, nil
		}
	}

	// if this is a non-standalone job, suspend the job if its parent workload is not found or not admitted.
	if !isStandaloneJob {
		_, _, finished := job.Finished()
		if !finished && !job.IsSuspended() {
			if parentWorkload, err := r.getParentWorkload(ctx, object); err != nil {
				log.Error(err, "couldn't get the parent job workload")
				return ctrl.Result{}, err
			} else if parentWorkload == nil || !workload.IsAdmitted(parentWorkload) {
				if err := clientutil.Patch(ctx, r.client, object, true, func() (bool, error) {
					job.Suspend()
					return true, nil
				}); err != nil {
					log.Error(err, "suspending child job failed")
					return ctrl.Result{}, err
				}
				r.record.Event(object, corev1.EventTypeNormal, ReasonSuspended, "Kueue managed child job suspended")
			}
		}
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling Job")

	// 1. make sure there is only a single existing instance of the workload.
	// If there's no workload exists and job is unsuspended, we'll stop it immediately.
	wl, err := r.ensureOneWorkload(ctx, job, object)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update workload conditions if implemented JobWithCustomWorkloadConditions interface.
	if jobCond, ok := job.(JobWithCustomWorkloadConditions); wl != nil && ok {
		if conditions, updated := jobCond.CustomWorkloadConditions(wl); updated {
			wlPatch := workload.BaseSSAWorkload(wl)
			wlPatch.Status.Conditions = conditions
			return reconcile.Result{}, r.client.Status().Patch(ctx, wlPatch, client.Apply,
				client.FieldOwner(fmt.Sprintf("%s-%s-controller", constants.KueueName, strings.ToLower(job.GVK().Kind))))
		}
	}

	if wl != nil && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
		if err := r.finalizeJob(ctx, job); err != nil {
			return ctrl.Result{}, err
		}

		r.record.Eventf(object, corev1.EventTypeNormal, ReasonFinishedWorkload,
			"Workload '%s' is declared finished", workload.Key(wl))
		return ctrl.Result{}, workload.RemoveFinalizer(ctx, r.client, wl)
	}

	// 1.1 If the workload is pending deletion, suspend the job if needed
	// and drop the finalizer.
	if wl != nil && !wl.DeletionTimestamp.IsZero() {
		log.V(2).Info("The workload is marked for deletion")
		err := r.stopJob(ctx, job, wl, StopReasonWorkloadDeleted, "Workload is deleted")
		if err != nil {
			log.Error(err, "Suspending job with deleted workload")
		} else {
			err = workload.RemoveFinalizer(ctx, r.client, wl)
		}
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if message, success, finished := job.Finished(); finished {
		log.V(3).Info("The workload is already finished")
		if wl != nil && !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			reason := kueue.WorkloadFinishedReasonSucceeded
			if !success {
				reason = kueue.WorkloadFinishedReasonFailed
			}
			err := workload.UpdateStatus(ctx, r.client, wl, kueue.WorkloadFinished, metav1.ConditionTrue, reason, message, constants.JobControllerName)
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			r.record.Eventf(object, corev1.EventTypeNormal, ReasonFinishedWorkload,
				"Workload '%s' is declared finished", workload.Key(wl))
		}

		// Execute job finalization logic
		if err := r.finalizeJob(ctx, job); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// 3. handle workload is nil.
	if wl == nil {
		log.V(3).Info("The workload is nil, handle job with no workload")
		err := r.handleJobWithNoWorkload(ctx, job, object)
		if err != nil {
			if IsUnretryableError(err) {
				log.V(3).Info("Handling job with no workload", "unretryableError", err)
			} else {
				log.Error(err, "Handling job with no workload")
			}
		}
		return ctrl.Result{}, err
	}

	// 4. update reclaimable counts if implemented by the job
	if jobRecl, implementsReclaimable := job.(JobWithReclaimablePods); implementsReclaimable {
		log.V(3).Info("update reclaimable counts if implemented by the job")
		reclPods, err := jobRecl.ReclaimablePods()
		if err != nil {
			log.Error(err, "Getting reclaimable pods")
			return ctrl.Result{}, err
		}

		if !workload.ReclaimablePodsAreEqual(reclPods, wl.Status.ReclaimablePods) {
			err = workload.UpdateReclaimablePods(ctx, r.client, wl, reclPods)
			if err != nil {
				log.Error(err, "Updating reclaimable pods")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	// 5. handle WaitForPodsReady only for a standalone job.
	// handle a job when waitForPodsReady is enabled, and it is the main job
	if r.waitForPodsReady {
		log.V(3).Info("Handling a job when waitForPodsReady is enabled")
		condition := generatePodsReadyCondition(job, wl)
		// optimization to avoid sending the update request if the status didn't change
		if !apimeta.IsStatusConditionPresentAndEqual(wl.Status.Conditions, condition.Type, condition.Status) {
			log.V(3).Info(fmt.Sprintf("Updating the PodsReady condition with status: %v", condition.Status))
			apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
			err := workload.UpdateStatus(ctx, r.client, wl, condition.Type, condition.Status, condition.Reason, condition.Message, constants.JobControllerName)
			if err != nil {
				log.Error(err, "Updating workload status")
			}
		}
	}

	// 6. handle eviction
	if evCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); evCond != nil && evCond.Status == metav1.ConditionTrue {
		log.V(3).Info("Handling a job with evicted condition")
		if err := r.stopJob(ctx, job, wl, StopReasonWorkloadEvicted, evCond.Message); err != nil {
			return ctrl.Result{}, err
		}
		if workload.HasQuotaReservation(wl) {
			if !job.IsActive() {
				log.V(6).Info("The job is no longer active, clear the workloads admission")
				// The requeued condition status set to true only on EvictedByPreemption or EvictedByAdmissionCheck
				setRequeued := evCond.Reason == kueue.WorkloadEvictedByPreemption || evCond.Reason == kueue.WorkloadEvictedByAdmissionCheck
				workload.SetRequeuedCondition(wl, evCond.Reason, evCond.Message, setRequeued)
				_ = workload.UnsetQuotaReservationWithCondition(wl, "Pending", evCond.Message)
				err := workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("clearing admission: %w", err)
				}
			}
		}
		return ctrl.Result{}, nil
	}

	// 7. handle job is suspended.
	if job.IsSuspended() {
		// start the job if the workload has been admitted, and the job is still suspended
		if workload.IsAdmitted(wl) {
			log.V(2).Info("Job admitted, unsuspending")
			err := r.startJob(ctx, job, object, wl)
			if err != nil {
				log.Error(err, "Unsuspending job")
				if podset.IsPermanent(err) {
					// Mark the workload as finished with failure since the is no point to retry.
					errUpdateStatus := workload.UpdateStatus(ctx, r.client, wl, kueue.WorkloadFinished, metav1.ConditionTrue, FailedToStartFinishedReason, err.Error(), constants.JobControllerName)
					if errUpdateStatus != nil {
						log.Error(errUpdateStatus, "Updating workload status, on start failure", "err", err)
					}
					return ctrl.Result{}, errUpdateStatus
				}
			}
			return ctrl.Result{}, err
		}

		if workload.HasQuotaReservation(wl) {
			r.recordAdmissionCheckUpdate(wl, job)
		}
		// update queue name if changed.
		q := QueueName(job)
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

	// 8. handle job is unsuspended.
	if !workload.IsAdmitted(wl) {
		// the job must be suspended if the workload is not yet admitted.
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		err := r.stopJob(ctx, job, wl, StopReasonNotAdmitted, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

func (r *JobReconciler) recordAdmissionCheckUpdate(wl *kueue.Workload, job GenericJob) {
	message := ""
	object := job.Object()
	for _, check := range wl.Status.AdmissionChecks {
		if check.State == kueue.CheckStatePending && check.Message != "" {
			if message != "" {
				message += "; "
			}
			message += check.Name + ": " + check.Message
		}
	}
	if message != "" {
		if cJob, isComposable := job.(ComposableJob); isComposable {
			cJob.ForEach(func(obj runtime.Object) {
				r.record.Eventf(obj, corev1.EventTypeNormal, ReasonUpdatedAdmissionCheck, message)
			})
		} else {
			r.record.Eventf(object, corev1.EventTypeNormal, ReasonUpdatedAdmissionCheck, message)
		}
	}
}

// IsParentJobManaged checks whether the parent job is managed by kueue.
func (r *JobReconciler) IsParentJobManaged(ctx context.Context, jobObj client.Object, namespace string) (bool, error) {
	owner := metav1.GetControllerOf(jobObj)
	if owner == nil {
		return false, nil
	}
	parentJob := GetEmptyOwnerObject(owner)
	if parentJob == nil {
		return false, fmt.Errorf("workload owner %v: %w", owner, ErrUnknownWorkloadOwner)
	}
	if err := r.client.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: namespace}, parentJob); err != nil {
		return false, errors.Join(ErrWorkloadOwnerNotFound, err)
	}
	return QueueNameForObject(parentJob) != "", nil
}

func (r *JobReconciler) getParentWorkload(ctx context.Context, object client.Object) (*kueue.Workload, error) {
	owner := metav1.GetControllerOf(object)
	if owner == nil {
		return nil, nil
	}
	wlList := kueue.WorkloadList{}
	if err := r.client.List(ctx, &wlList, client.InNamespace(object.GetNamespace()), client.MatchingFields{indexer.OwnerReferenceUID: string(owner.UID)}); err != nil || len(wlList.Items) == 0 {
		return nil, client.IgnoreNotFound(err)
	}

	// In theory the parent can own multiple Workloads, we cannot do too much about it, maybe log it.
	return &wlList.Items[0], nil
}

// ensureOneWorkload will query for the single matched workload corresponding to job and return it.
// If there are more than one workload, we should delete the excess ones.
// The returned workload could be nil.
func (r *JobReconciler) ensureOneWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	if prebuiltWorkloadName, usePrebuiltWorkload := PrebuiltWorkloadFor(job); usePrebuiltWorkload {
		wl := &kueue.Workload{}
		err := r.client.Get(ctx, types.NamespacedName{Name: prebuiltWorkloadName, Namespace: object.GetNamespace()}, wl)
		if err != nil {
			return nil, client.IgnoreNotFound(err)
		}

		if owns, err := r.ensurePrebuiltWorkloadOwnership(ctx, wl, object); !owns || err != nil {
			return nil, err
		}

		if inSync, err := r.ensurePrebuiltWorkloadInSync(ctx, wl, job); !inSync || err != nil {
			return nil, err
		}
		return wl, nil
	}

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload
	if cj, implements := job.(ComposableJob); implements {
		var err error
		match, toDelete, err = cj.FindMatchingWorkloads(ctx, r.client, r.record)
		if err != nil {
			log.Error(err, "Composable job is unable to find matching workloads")
			return nil, err
		}
	} else {
		var err error
		match, toDelete, err = FindMatchingWorkloads(ctx, r.client, job)
		if err != nil {
			log.Error(err, "Unable to list child workloads")
			return nil, err
		}
	}

	var toUpdate *kueue.Workload
	if match == nil && len(toDelete) > 0 && job.IsSuspended() && !workload.HasQuotaReservation(toDelete[0]) {
		toUpdate = toDelete[0]
		toDelete = toDelete[1:]
	}

	// If there is no matching workload and the job is running, suspend it.
	if match == nil && !job.IsSuspended() {
		log.V(2).Info("job with no matching workload, suspending")
		var w *kueue.Workload
		if len(toDelete) == 1 {
			// The job may have been modified and hence the existing workload
			// doesn't match the job anymore. All bets are off if there are more
			// than one workload...
			w = toDelete[0]
		}

		if _, _, finished := job.Finished(); !finished {
			var msg string
			if w == nil {
				msg = "Missing Workload; unable to restore pod templates"
			} else {
				msg = "No matching Workload; restoring pod templates according to existent Workload"
			}
			if err := r.stopJob(ctx, job, w, StopReasonNoMatchingWorkload, msg); err != nil {
				return nil, fmt.Errorf("stopping job with no matching workload: %w", err)
			}
		}
	}

	// Delete duplicate workload instances.
	existedWls := 0
	for _, wl := range toDelete {
		wlKey := workload.Key(wl)
		err := workload.RemoveFinalizer(ctx, r.client, wl)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to remove workload finalizer for: %w ", err)
		}

		err = r.client.Delete(ctx, wl)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("deleting not matching workload: %w", err)
		}
		if err == nil {
			existedWls++
			r.record.Eventf(object, corev1.EventTypeNormal, ReasonDeletedWorkload,
				"Deleted not matching Workload: %v", wlKey)
		}
	}

	if existedWls != 0 {
		if match == nil {
			return nil, fmt.Errorf("%w: deleted %d workloads", ErrNoMatchingWorkloads, len(toDelete))
		}
		return nil, fmt.Errorf("%w: deleted %d workloads", ErrExtraWorkloads, len(toDelete))
	}

	if toUpdate != nil {
		return r.updateWorkloadToMatchJob(ctx, job, object, toUpdate)
	}

	return match, nil
}

func FindMatchingWorkloads(ctx context.Context, c client.Client, job GenericJob) (match *kueue.Workload, toDelete []*kueue.Workload, err error) {
	object := job.Object()

	workloads := &kueue.WorkloadList{}
	if err := c.List(ctx, workloads, client.InNamespace(object.GetNamespace()),
		client.MatchingFields{GetOwnerKey(job.GVK()): object.GetName()}); err != nil {
		return nil, nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		if match == nil && equivalentToWorkload(ctx, c, job, w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	return match, toDelete, nil
}

func (r *JobReconciler) ensurePrebuiltWorkloadOwnership(ctx context.Context, wl *kueue.Workload, object client.Object) (bool, error) {
	if !metav1.IsControlledBy(wl, object) {
		if err := ctrl.SetControllerReference(object, wl, r.client.Scheme()); err != nil {
			// don't return an error here, since a retry cannot give a different result,
			// log the error.
			log := ctrl.LoggerFrom(ctx)
			log.Error(err, "Cannot take ownership of the workload")
			return false, nil
		}

		if errs := validation.IsValidLabelValue(string(object.GetUID())); len(errs) == 0 {
			wl.Labels = maps.MergeKeepFirst(map[string]string{controllerconsts.JobUIDLabel: string(object.GetUID())}, wl.Labels)
		}

		if err := r.client.Update(ctx, wl); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *JobReconciler) ensurePrebuiltWorkloadInSync(ctx context.Context, wl *kueue.Workload, job GenericJob) (bool, error) {
	if !equivalentToWorkload(ctx, r.client, job, wl) {
		// mark the workload as finished
		err := workload.UpdateStatus(ctx, r.client, wl,
			kueue.WorkloadFinished,
			metav1.ConditionTrue,
			kueue.WorkloadFinishedReasonOutOfSync,
			"The prebuilt workload is out of sync with its user job",
			constants.JobControllerName)
		return false, err
	}
	return true, nil
}

// expectedRunningPodSets gets the expected podsets during the job execution, returns nil if the workload has no reservation or
// the admission does not match.
func expectedRunningPodSets(ctx context.Context, c client.Client, wl *kueue.Workload) []kueue.PodSet {
	if !workload.HasQuotaReservation(wl) {
		return nil
	}
	info, err := getPodSetsInfoFromStatus(ctx, c, wl)
	if err != nil {
		return nil
	}
	infoMap := slices.ToRefMap(info, func(psi *podset.PodSetInfo) string { return psi.Name })
	runningPodSets := wl.Spec.DeepCopy().PodSets
	canBePartiallyAdmitted := workload.CanBePartiallyAdmitted(wl)
	for i := range runningPodSets {
		ps := &runningPodSets[i]
		psi, found := infoMap[ps.Name]
		if !found {
			return nil
		}
		err := podset.Merge(&ps.Template.ObjectMeta, &ps.Template.Spec, *psi)
		if err != nil {
			return nil
		}
		if canBePartiallyAdmitted && ps.MinCount != nil {
			// update the expected running count
			ps.Count = psi.Count
		}
	}
	return runningPodSets
}

// equivalentToWorkload checks if the job corresponds to the workload
func equivalentToWorkload(ctx context.Context, c client.Client, job GenericJob, wl *kueue.Workload) bool {
	owner := metav1.GetControllerOf(wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != job.Object().GetName() {
		return false
	}

	jobPodSets := clearMinCountsIfFeatureDisabled(job.PodSets())

	if runningPodSets := expectedRunningPodSets(ctx, c, wl); runningPodSets != nil {
		if equality.ComparePodSetSlices(jobPodSets, runningPodSets, workload.IsAdmitted(wl)) {
			return true
		}
		// If the workload is admitted but the job is suspended, do the check
		// against the non-running info.
		// This might allow some violating jobs to pass equivalency checks, but their
		// workloads would be invalidated in the next sync after unsuspending.
		return job.IsSuspended() && equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, workload.IsAdmitted(wl))
	}

	return equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, workload.IsAdmitted(wl))
}

func (r *JobReconciler) updateWorkloadToMatchJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) (*kueue.Workload, error) {
	newWl, err := r.constructWorkload(ctx, job, object)
	if err != nil {
		return nil, fmt.Errorf("can't construct workload for update: %w", err)
	}
	err = r.prepareWorkload(ctx, job, newWl)
	if err != nil {
		return nil, fmt.Errorf("can't construct workload for update: %w", err)
	}
	wl.Spec = newWl.Spec
	if err = r.client.Update(ctx, wl); err != nil {
		return nil, fmt.Errorf("updating existed workload: %w", err)
	}

	r.record.Eventf(object, corev1.EventTypeNormal, ReasonUpdatedWorkload,
		"Updated not matching Workload for suspended job: %v", klog.KObj(wl))
	return newWl, nil
}

// startJob will unsuspend the job, and also inject the node affinity.
func (r *JobReconciler) startJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) error {
	info, err := getPodSetsInfoFromStatus(ctx, r.client, wl)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Admitted by clusterQueue %v", wl.Status.Admission.ClusterQueue)

	if cj, implements := job.(ComposableJob); implements {
		if err := cj.Run(ctx, r.client, info, r.record, msg); err != nil {
			return err
		}
	} else {
		if err := clientutil.Patch(ctx, r.client, object, true, func() (bool, error) {
			return true, job.RunWithPodSetsInfo(info)
		}); err != nil {
			return err
		}
		r.record.Event(object, corev1.EventTypeNormal, ReasonStarted, msg)
	}

	return nil
}

// stopJob will suspend the job, and also restore node affinity, reset job status if needed.
// Returns whether any operation was done to stop the job or an error.
func (r *JobReconciler) stopJob(ctx context.Context, job GenericJob, wl *kueue.Workload, stopReason StopReason, eventMsg string) error {
	object := job.Object()

	info := GetPodSetsInfoFromWorkload(wl)

	if jws, implements := job.(JobWithCustomStop); implements {
		stoppedNow, err := jws.Stop(ctx, r.client, info, stopReason, eventMsg)
		if stoppedNow {
			r.record.Event(object, corev1.EventTypeNormal, ReasonStopped, eventMsg)
		}
		return err
	}

	if jws, implements := job.(ComposableJob); implements {
		reason := stopReason
		if stopReason == StopReasonWorkloadEvicted {
			if evCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); evCond != nil && evCond.Status == metav1.ConditionTrue {
				reason = StopReason(string(StopReasonWorkloadEvicted) + "DueTo" + evCond.Reason)
			}
		}
		stoppedNow, err := jws.Stop(ctx, r.client, info, reason, eventMsg)
		for _, objStoppedNow := range stoppedNow {
			r.record.Event(objStoppedNow, corev1.EventTypeNormal, ReasonStopped, eventMsg)
		}
		return err
	}

	if job.IsSuspended() {
		return nil
	}

	if err := clientutil.Patch(ctx, r.client, object, true, func() (bool, error) {
		job.Suspend()
		if info != nil {
			job.RestorePodSetsInfo(info)
		}
		return true, nil
	}); err != nil {
		return err
	}

	r.record.Event(object, corev1.EventTypeNormal, ReasonStopped, eventMsg)
	return nil
}

func (r *JobReconciler) finalizeJob(ctx context.Context, job GenericJob) error {
	if jwf, implements := job.(JobWithFinalize); implements {
		if err := jwf.Finalize(ctx, r.client); err != nil {
			return err
		}
	}

	return nil
}

// constructWorkload will derive a workload from the corresponding job.
func (r *JobReconciler) constructWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	if cj, implements := job.(ComposableJob); implements {
		wl, err := cj.ConstructComposableWorkload(ctx, r.client, r.record, r.labelKeysToCopy)
		if err != nil {
			return nil, err
		}
		return wl, nil
	}

	podSets := job.PodSets()

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:        GetWorkloadNameForOwnerWithGVK(object.GetName(), object.GetUID(), job.GVK()),
			Namespace:   object.GetNamespace(),
			Labels:      maps.FilterKeys(job.Object().GetLabels(), r.labelKeysToCopy),
			Finalizers:  []string{kueue.ResourceInUseFinalizerName},
			Annotations: admissioncheck.FilterProvReqAnnotations(job.Object().GetAnnotations()),
		},
		Spec: kueue.WorkloadSpec{
			PodSets:   podSets,
			QueueName: QueueName(job),
		},
	}
	if wl.Labels == nil {
		wl.Labels = make(map[string]string)
	}
	jobUID := string(job.Object().GetUID())
	if errs := validation.IsValidLabelValue(jobUID); len(errs) == 0 {
		wl.Labels[controllerconsts.JobUIDLabel] = jobUID
	} else {
		log.V(2).Info(
			"Validation of the owner job UID label has failed. Creating workload without the label.",
			"ValidationErrors", errs,
			"LabelValue", jobUID,
		)
	}

	if err := ctrl.SetControllerReference(object, wl, r.client.Scheme()); err != nil {
		return nil, err
	}
	return wl, nil
}

// prepareWorkload adds the priority information for the constructed workload
func (r *JobReconciler) prepareWorkload(ctx context.Context, job GenericJob, wl *kueue.Workload) error {
	priorityClassName, source, p, err := r.extractPriority(ctx, wl.Spec.PodSets, job)
	if err != nil {
		return err
	}

	wl.Spec.PriorityClassName = priorityClassName
	wl.Spec.Priority = &p
	wl.Spec.PriorityClassSource = source

	wl.Spec.PodSets = clearMinCountsIfFeatureDisabled(wl.Spec.PodSets)

	return nil
}

func (r *JobReconciler) extractPriority(ctx context.Context, podSets []kueue.PodSet, job GenericJob) (string, string, int32, error) {
	if workloadPriorityClass := workloadPriorityClassName(job); len(workloadPriorityClass) > 0 {
		return utilpriority.GetPriorityFromWorkloadPriorityClass(ctx, r.client, workloadPriorityClass)
	}
	if jobWithPriorityClass, isImplemented := job.(JobWithPriorityClass); isImplemented {
		return utilpriority.GetPriorityFromPriorityClass(
			ctx, r.client, jobWithPriorityClass.PriorityClass())
	}
	return utilpriority.GetPriorityFromPriorityClass(
		ctx, r.client, extractPriorityFromPodSets(podSets))
}

func extractPriorityFromPodSets(podSets []kueue.PodSet) string {
	for _, podSet := range podSets {
		if len(podSet.Template.Spec.PriorityClassName) > 0 {
			return podSet.Template.Spec.PriorityClassName
		}
	}
	return ""
}

// getPodSetsInfoFromStatus extracts podSetsInfo from workload status, based on
// admission, and admission checks.
func getPodSetsInfoFromStatus(ctx context.Context, c client.Client, w *kueue.Workload) ([]podset.PodSetInfo, error) {
	if len(w.Status.Admission.PodSetAssignments) == 0 {
		return nil, nil
	}

	podSetsInfo := make([]podset.PodSetInfo, len(w.Status.Admission.PodSetAssignments))

	for i, podSetFlavor := range w.Status.Admission.PodSetAssignments {
		info, err := podset.FromAssignment(ctx, c, &podSetFlavor, w.Spec.PodSets[i].Count)
		if err != nil {
			return nil, err
		}

		for _, admissionCheck := range w.Status.AdmissionChecks {
			for _, podSetUpdate := range admissionCheck.PodSetUpdates {
				if podSetUpdate.Name == info.Name {
					if err := info.Merge(podset.FromUpdate(&podSetUpdate)); err != nil {
						return nil, fmt.Errorf("in admission check %q: %w", admissionCheck.Name, err)
					}
					break
				}
			}
		}
		podSetsInfo[i] = info
	}
	return podSetsInfo, nil
}

func (r *JobReconciler) handleJobWithNoWorkload(ctx context.Context, job GenericJob, object client.Object) error {
	log := ctrl.LoggerFrom(ctx)

	_, usePrebuiltWorkload := PrebuiltWorkloadFor(job)
	if usePrebuiltWorkload {
		// Stop the job if not already suspended
		if stopErr := r.stopJob(ctx, job, nil, StopReasonNoMatchingWorkload, "missing workload"); stopErr != nil {
			return stopErr
		}
	}

	// Wait until there are no active pods.
	if job.IsActive() {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	if usePrebuiltWorkload {
		log.V(2).Info("Skip workload creation for job with prebuilt workload")
		return nil
	}

	// Create the corresponding workload.
	wl, err := r.constructWorkload(ctx, job, object)
	if err != nil {
		return err
	}
	err = r.prepareWorkload(ctx, job, wl)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}
	r.record.Eventf(object, corev1.EventTypeNormal, ReasonCreatedWorkload,
		"Created Workload: %v", workload.Key(wl))
	return nil
}

func (r *JobReconciler) ignoreUnretryableError(log logr.Logger, err error) error {
	if IsUnretryableError(err) {
		log.V(2).Info("Received an unretryable error", "error", err)
		return nil
	}
	return err
}

func generatePodsReadyCondition(job GenericJob, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the condition when the pods transition
	// from Ready to Completed. As pods finish, they transition first into the
	// uncountedTerminatedPods staging area, before passing to the
	// succeeded/failed counters.
	if workload.IsAdmitted(wl) && (job.PodsReady() || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
		conditionStatus = metav1.ConditionTrue
		message = "All pods were ready or succeeded since the workload admission"
	}
	return metav1.Condition{
		Type:    kueue.WorkloadPodsReady,
		Status:  conditionStatus,
		Reason:  "PodsReady",
		Message: message,
		// ObservedGeneration is added via workload.UpdateStatus
	}
}

// GetPodSetsInfoFromWorkload retrieve the podSetsInfo slice from the
// provided workload's spec
func GetPodSetsInfoFromWorkload(wl *kueue.Workload) []podset.PodSetInfo {
	if wl == nil {
		return nil
	}

	return slices.Map(wl.Spec.PodSets, podset.FromPodSet)
}

type ReconcilerSetup func(*builder.Builder, client.Client) *builder.Builder

// NewGenericReconcilerFactory creates a new reconciler factory for a concrete GenericJob type.
// newJob should return a new empty job.
func NewGenericReconcilerFactory(newJob func() GenericJob, setup ...ReconcilerSetup) ReconcilerFactory {
	return func(client client.Client, record record.EventRecorder, opts ...Option) JobReconcilerInterface {
		return &genericReconciler{
			jr:     NewReconciler(client, record, opts...),
			newJob: newJob,
			setup:  setup,
		}
	}
}

type genericReconciler struct {
	jr     *JobReconciler
	newJob func() GenericJob
	setup  []ReconcilerSetup
}

func (r *genericReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jr.ReconcileGenericJob(ctx, req, r.newJob())
}

func (r *genericReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(r.newJob().Object()).Owns(&kueue.Workload{})
	c := mgr.GetClient()
	for _, f := range r.setup {
		b = f(b, c)
	}
	return b.Complete(r)
}

// clearMinCountsIfFeatureDisabled sets the minCount for all podSets to nil if the PartialAdmission feature is not enabled
func clearMinCountsIfFeatureDisabled(in []kueue.PodSet) []kueue.PodSet {
	if features.Enabled(features.PartialAdmission) || len(in) == 0 {
		return in
	}
	for i := range in {
		in[i].MinCount = nil
	}
	return in
}
