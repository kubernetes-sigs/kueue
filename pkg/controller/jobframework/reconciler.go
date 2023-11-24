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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	FailedToStartFinishedReason = "FailedToStart"
)

var (
	ErrChildJobOwnerNotFound = fmt.Errorf("owner isn't set even though %s annotation is set", controllerconsts.ParentWorkloadAnnotation)
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
}

type Options struct {
	ManageJobsWithoutQueueName bool
	WaitForPodsReady           bool
	KubeServerVersion          *kubeversion.ServerVersionFetcher
	PodNamespaceSelector       *metav1.LabelSelector
	PodSelector                *metav1.LabelSelector
}

// Option configures the reconciler.
type Option func(*Options)

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
func WithWaitForPodsReady(f bool) Option {
	return func(o *Options) {
		o.WaitForPodsReady = f
	}
}

func WithKubeServerVersion(v *kubeversion.ServerVersionFetcher) Option {
	return func(o *Options) {
		o.KubeServerVersion = v
	}
}

// WithPodNamespaceSelector adds rules to reconcile pods only in particular
// namespaces.
func WithPodNamespaceSelector(s *metav1.LabelSelector) Option {
	return func(o *Options) {
		o.PodNamespaceSelector = s
	}
}

// WithPodSelector adds rules to reconcile pods only with particular
// labels.
func WithPodSelector(s *metav1.LabelSelector) Option {
	return func(o *Options) {
		o.PodSelector = s
	}
}

var DefaultOptions = Options{}

func NewReconciler(
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *JobReconciler {
	options := DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &JobReconciler{
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		waitForPodsReady:           options.WaitForPodsReady,
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
		dropFinalizers, err = cJob.Load(ctx, r.client, req.NamespacedName)
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
		workloads := kueue.WorkloadList{}
		if err := r.client.List(ctx, &workloads, client.InNamespace(req.Namespace),
			client.MatchingFields{getOwnerKey(job.GVK()): req.Name}); err != nil {
			log.Error(err, "Unable to list child workloads")
			return ctrl.Result{}, err
		}
		for i := range workloads.Items {
			err := r.removeFinalizer(ctx, &workloads.Items[i])
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

	isStandaloneJob := ParentWorkloadName(job) == ""

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have either
	// queue-name or the parent-workload annotation set.
	// If the parent-workload annotation is set, it also checks whether the parent job has queue-name label.
	if !r.manageJobsWithoutQueueName && QueueName(job) == "" {
		if isStandaloneJob {
			log.V(3).Info("Neither queue-name label, nor parent-workload annotation is set, ignoring the job",
				"queueName", QueueName(job), "parentWorkload", ParentWorkloadName(job))
			return ctrl.Result{}, nil
		}
		isParentJobManaged, err := r.IsParentJobManaged(ctx, job.Object(), req.Namespace)
		if err != nil {
			log.Error(err, "couldn't check whether the parent job is managed by kueue")
			return ctrl.Result{}, err
		}
		if !isParentJobManaged {
			log.V(3).Info("parent-workload annotation is set, and the parent job doesn't have a queue-name label, ignoring the job",
				"parentWorkload", ParentWorkloadName(job))
			return ctrl.Result{}, nil
		}
	}

	// if this is a non-standalone job, suspend the job if its parent workload is not found or not admitted.
	if !isStandaloneJob {
		_, finished := job.Finished()
		if !finished && !job.IsSuspended() {
			if parentWorkload, err := r.getParentWorkload(ctx, job, object); err != nil {
				log.Error(err, "couldn't get the parent job workload")
				return ctrl.Result{}, err
			} else if parentWorkload == nil || !workload.IsAdmitted(parentWorkload) {
				// suspend it
				job.Suspend()
				if err := r.client.Update(ctx, object); err != nil {
					log.Error(err, "suspending child job failed")
					return ctrl.Result{}, err
				}
				r.record.Eventf(object, corev1.EventTypeNormal, "Suspended", "Kueue managed child job suspended")
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

	if wl != nil && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
		return ctrl.Result{}, r.removeFinalizer(ctx, wl)
	}

	// 1.1 If the workload is pending deletion, suspend the job if needed
	// and drop the finalizer.
	if wl != nil && !wl.DeletionTimestamp.IsZero() {
		log.V(2).Info("The workload is marked for deletion")
		err := r.stopJob(ctx, job, object, wl, "Workload is deleted")
		if err != nil {
			log.Error(err, "Suspending job with deleted workload")
		}

		if err == nil && wl != nil {
			err = r.removeFinalizer(ctx, wl)
		}
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if condition, finished := job.Finished(); finished && wl != nil {
		// Execute job finalization logic
		if err := r.finalizeJob(ctx, job); err != nil {
			return ctrl.Result{}, err
		}

		err := workload.UpdateStatus(ctx, r.client, wl, condition.Type, condition.Status, condition.Reason, condition.Message, constants.JobControllerName)
		if err != nil {
			log.Error(err, "Updating workload status")
		}

		return ctrl.Result{}, nil
	}

	// 3. handle workload is nil.
	if wl == nil {
		err := r.handleJobWithNoWorkload(ctx, job, object)
		if err != nil {
			log.Error(err, "Handling job with no workload")
		}
		return ctrl.Result{}, err
	}

	// 4. update reclaimable counts if implemented by the job
	if jobRecl, implementsReclaimable := job.(JobWithReclaimablePods); implementsReclaimable {
		if rp := jobRecl.ReclaimablePods(); !workload.ReclaimablePodsAreEqual(rp, wl.Status.ReclaimablePods) {
			err = workload.UpdateReclaimablePods(ctx, r.client, wl, rp)
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
		log.V(5).Info("Handling a job when waitForPodsReady is enabled")
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
		if err := r.stopJob(ctx, job, object, wl, evCond.Message); err != nil {
			return ctrl.Result{}, err
		}
		if workload.HasQuotaReservation(wl) {
			if !job.IsActive() {
				log.V(6).Info("The job is no longer active, clear the workloads admission")
				workload.UnsetQuotaReservationWithCondition(wl, "Pending", evCond.Message)
				_ = workload.SyncAdmittedCondition(wl)
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
						log.Error(errUpdateStatus, "Updating workload status, on start failure %s", err.Error())
					}
					return ctrl.Result{}, errUpdateStatus
				}
			}
			return ctrl.Result{}, err
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
		err := r.stopJob(ctx, job, object, wl, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

// IsParentJobManaged checks whether the parent job is managed by kueue.
func (r *JobReconciler) IsParentJobManaged(ctx context.Context, jobObj client.Object, namespace string) (bool, error) {
	owner := metav1.GetControllerOf(jobObj)
	if owner == nil {
		return false, ErrChildJobOwnerNotFound
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

func (r *JobReconciler) getParentWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	pw := kueue.Workload{}
	namespacedName := types.NamespacedName{
		Name:      ParentWorkloadName(job),
		Namespace: object.GetNamespace(),
	}
	if err := r.client.Get(ctx, namespacedName, &pw); err != nil {
		return nil, client.IgnoreNotFound(err)
	} else {
		return &pw, nil
	}
}

// ensureOneWorkload will query for the single matched workload corresponding to job and return it.
// If there are more than one workload, we should delete the excess ones.
// The returned workload could be nil.
func (r *JobReconciler) ensureOneWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload
	if cj, implements := job.(ComposableJob); implements {
		var err error
		match, toDelete, err = cj.FindMatchingWorkloads(ctx, r.client)
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

		if _, finished := job.Finished(); finished {
			if err := r.finalizeJob(ctx, job); err != nil {
				return nil, fmt.Errorf("finalizing job with no matching workload: %w", err)
			}
		} else {
			if err := r.stopJob(ctx, job, object, w, "No matching Workload"); err != nil {
				return nil, fmt.Errorf("stopping job with no matching workload: %w", err)
			}
		}
	}

	// Delete duplicate workload instances.
	existedWls := 0
	for _, wl := range toDelete {
		wlKey := workload.Key(wl)
		err := r.removeFinalizer(ctx, wl)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to remove workload finalizer for: %w ", err)
		}

		err = r.client.Delete(ctx, wl)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("deleting not matching workload: %w", err)
		}
		if err == nil {
			existedWls++
			r.record.Eventf(object, corev1.EventTypeNormal, "DeletedWorkload",
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
		client.MatchingFields{getOwnerKey(job.GVK()): object.GetName()}); err != nil {
		return nil, nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		if match == nil && equivalentToWorkload(job, w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	return match, toDelete, nil
}

// equivalentToWorkload checks if the job corresponds to the workload
func equivalentToWorkload(job GenericJob, wl *kueue.Workload) bool {
	owner := metav1.GetControllerOf(wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != job.Object().GetName() {
		return false
	}

	jobPodSets := clearMinCountsIfFeatureDisabled(job.PodSets())

	if !workload.CanBePartiallyAdmitted(wl) || !workload.HasQuotaReservation(wl) {
		// the two sets should fully match.
		return equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, true)
	}

	// Check everything but the pod counts.
	if !equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, false) {
		return false
	}

	// If the workload is admitted but the job is suspended, ignore counts.
	// This might allow some violating jobs to pass equivalency checks, but their
	// workloads would be invalidated in the next sync after unsuspending.
	if job.IsSuspended() {
		return true
	}

	for i, psAssigment := range wl.Status.Admission.PodSetAssignments {
		assignedCount := wl.Spec.PodSets[i].Count
		if jobPodSets[i].MinCount != nil {
			assignedCount = ptr.Deref(psAssigment.Count, assignedCount)
		}
		if jobPodSets[i].Count != assignedCount {
			return false
		}
	}
	return true
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

	r.record.Eventf(object, corev1.EventTypeNormal, "UpdatedWorkload",
		"Updated not matching Workload for suspended job: %v", wl)
	return newWl, nil
}

// startJob will unsuspend the job, and also inject the node affinity.
func (r *JobReconciler) startJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) error {
	info, err := r.getPodSetsInfoFromStatus(ctx, wl)
	if err != nil {
		return err
	}
	if runErr := job.RunWithPodSetsInfo(info); runErr != nil {
		return runErr
	}

	if err := r.client.Update(ctx, object); err != nil {
		return err
	}

	r.record.Eventf(object, corev1.EventTypeNormal, "Started",
		"Admitted by clusterQueue %v", wl.Status.Admission.ClusterQueue)

	return nil
}

// stopJob will suspend the job, and also restore node affinity, reset job status if needed.
// Returns whether any operation was done to stop the job or an error.
func (r *JobReconciler) stopJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload, eventMsg string) error {
	info := GetPodSetsInfoFromWorkload(wl)

	if jws, implements := job.(JobWithCustomStop); implements {
		stoppedNow, err := jws.Stop(ctx, r.client, info, eventMsg)
		if stoppedNow {
			r.record.Eventf(object, corev1.EventTypeNormal, "Stopped", eventMsg)
		}
		return err
	}

	if job.IsSuspended() {
		return nil
	}

	job.Suspend()
	if info != nil {
		job.RestorePodSetsInfo(info)
	}
	if err := r.client.Update(ctx, object); err != nil {
		return err
	}

	r.record.Eventf(object, corev1.EventTypeNormal, "Stopped", eventMsg)
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

func (r *JobReconciler) removeFinalizer(ctx context.Context, wl *kueue.Workload) error {
	if controllerutil.RemoveFinalizer(wl, kueue.ResourceInUseFinalizerName) {
		return r.client.Update(ctx, wl)
	}
	return nil
}

// constructWorkload will derive a workload from the corresponding job.
func (r *JobReconciler) constructWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	if cj, implements := job.(ComposableJob); implements {
		wl, err := cj.ConstructComposableWorkload(ctx, r.client, r.record)
		if err != nil {
			return nil, err
		}

		return wl, nil
	}

	podSets := job.PodSets()

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:       GetWorkloadNameForOwnerWithGVK(object.GetName(), job.GVK()),
			Namespace:  object.GetNamespace(),
			Labels:     map[string]string{},
			Finalizers: []string{kueue.ResourceInUseFinalizerName},
		},
		Spec: kueue.WorkloadSpec{
			PodSets:   podSets,
			QueueName: QueueName(job),
		},
	}

	jobUid := string(job.Object().GetUID())
	if errs := validation.IsValidLabelValue(jobUid); len(errs) == 0 {
		wl.Labels[controllerconsts.JobUIDLabel] = jobUid
	} else {
		log.V(2).Info(
			"Validation of the owner job UID label has failed. Creating workload without the label.",
			"ValidationErrors", errs,
			"LabelValue", jobUid,
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
func (r *JobReconciler) getPodSetsInfoFromStatus(ctx context.Context, w *kueue.Workload) ([]podset.PodSetInfo, error) {
	if len(w.Status.Admission.PodSetAssignments) == 0 {
		return nil, nil
	}

	podSetsInfo := make([]podset.PodSetInfo, len(w.Status.Admission.PodSetAssignments))

	for i, podSetFlavor := range w.Status.Admission.PodSetAssignments {
		info, err := podset.FromAssignment(ctx, r.client, &podSetFlavor, w.Spec.PodSets[i].Count)
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

	// Wait until there are no active pods.
	if job.IsActive() {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
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
	r.record.Eventf(object, corev1.EventTypeNormal, "CreatedWorkload",
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
	}
}

// GetPodSetsInfoFromWorkload retrieve the podSetsInfo slice from the
// provided workload's spec
func GetPodSetsInfoFromWorkload(wl *kueue.Workload) []podset.PodSetInfo {
	if wl == nil {
		return nil
	}

	return utilslices.Map(wl.Spec.PodSets, podset.FromPodSet)

}

// NewGenericReconciler creates a new reconciler factory for a concrete GenericJob type.
// newJob should return a new empty job.
// newWorkloadHandler it's optional, if added it should return a new workload event handler.
func NewGenericReconciler(newJob func() GenericJob, newWorkloadHandler func(client.Client) handler.EventHandler) ReconcilerFactory {
	return func(client client.Client, record record.EventRecorder, opts ...Option) JobReconcilerInterface {
		return &genericReconciler{
			jr:                 NewReconciler(client, record, opts...),
			newJob:             newJob,
			newWorkloadHandler: newWorkloadHandler,
		}
	}
}

type genericReconciler struct {
	jr                 *JobReconciler
	newJob             func() GenericJob
	newWorkloadHandler func(client.Client) handler.EventHandler
}

func (r *genericReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jr.ReconcileGenericJob(ctx, req, r.newJob())
}

func (r *genericReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(r.newJob().Object()).
		Owns(&kueue.Workload{}, builder.MatchEveryOwner)
	if r.newWorkloadHandler != nil {
		b = b.Watches(&kueue.Workload{}, r.newWorkloadHandler(mgr.GetClient()))
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
