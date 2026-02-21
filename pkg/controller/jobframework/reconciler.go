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

package jobframework

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	FailedToStartFinishedReason = "FailedToStart"
	managedOwnersChainLimit     = 10
)

var (
	ErrCyclicOwnership                = errors.New("cyclic ownership")
	ErrWorkloadOwnerNotFound          = errors.New("workload owner not found")
	ErrManagedOwnersChainLimitReached = errors.New("managed owner chain limit reached")
	ErrNoMatchingWorkloads            = errors.New("no matching workloads")
	ErrExtraWorkloads                 = errors.New("extra workloads")
	ErrPrebuiltWorkloadNotFound       = errors.New("prebuilt workload not found")
)

type WorkloadRetentionPolicy struct {
	AfterDeactivatedByKueue *time.Duration
}

// JobReconciler reconciles a GenericJob object
type JobReconciler struct {
	client                       client.Client
	record                       record.EventRecorder
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	waitForPodsReady             bool
	labelKeysToCopy              []string
	clock                        clock.Clock
	workloadRetentionPolicy      WorkloadRetentionPolicy
	roleTracker                  *roletracker.RoleTracker
}

// RoleTracker returns the role tracker for HA logging.
func (r *JobReconciler) RoleTracker() *roletracker.RoleTracker {
	return r.roleTracker
}

type Options struct {
	ManageJobsWithoutQueueName   bool
	ManagedJobsNamespaceSelector labels.Selector
	WaitForPodsReady             bool
	KubeServerVersion            *kubeversion.ServerVersionFetcher
	IntegrationOptions           map[string]any // IntegrationOptions key is "$GROUP/$VERSION, Kind=$KIND".
	EnabledFrameworks            sets.Set[string]
	EnabledExternalFrameworks    sets.Set[string]
	ManagerName                  string
	LabelKeysToCopy              []string
	Queues                       *qcache.Manager
	Cache                        *schdcache.Cache
	Clock                        clock.Clock
	WorkloadRetentionPolicy      WorkloadRetentionPolicy
	RoleTracker                  *roletracker.RoleTracker
	NoopWebhook                  bool
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

// WithManagedJobsNamespaceSelector is used for namespace-based filtering of ManagedJobsWithoutQueueName
func WithManagedJobsNamespaceSelector(ls labels.Selector) Option {
	return func(o *Options) {
		o.ManagedJobsNamespaceSelector = ls
	}
}

// WithWaitForPodsReady indicates if the controller should add the PodsReady
// condition to the workload when the corresponding job has all pods ready
// or succeeded.
func WithWaitForPodsReady(cfg *configapi.WaitForPodsReady) Option {
	return func(o *Options) {
		o.WaitForPodsReady = waitforpodsready.Enabled(cfg)
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
func WithQueues(q *qcache.Manager) Option {
	return func(o *Options) {
		o.Queues = q
	}
}

// WithCache adds the cache manager.
func WithCache(c *schdcache.Cache) Option {
	return func(o *Options) {
		o.Cache = c
	}
}

// WithClock sets the clock of the reconciler.
func WithClock(c clock.Clock) Option {
	return func(o *Options) {
		o.Clock = c
	}
}

// WithObjectRetentionPolicies sets DeactivationRetentionPeriod policy.
func WithObjectRetentionPolicies(value *configapi.ObjectRetentionPolicies) Option {
	return func(o *Options) {
		if value != nil && value.Workloads != nil && value.Workloads.AfterDeactivatedByKueue != nil {
			o.WorkloadRetentionPolicy.AfterDeactivatedByKueue = &value.Workloads.AfterDeactivatedByKueue.Duration
		}
	}
}

// WithRoleTracker sets the roleTracker for HA logging.
func WithRoleTracker(tracker *roletracker.RoleTracker) Option {
	return func(o *Options) {
		o.RoleTracker = tracker
	}
}

// WithNoopWebhook sets the integration webhook to noopWebhook.
// This is needed when the integration is disabled.
func WithNoopWebhook(noop bool) Option {
	return func(o *Options) {
		o.NoopWebhook = noop
	}
}

var defaultOptions = Options{
	Clock: clock.RealClock{},
}

func NewReconciler(
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *JobReconciler {
	options := ProcessOptions(opts...)

	return &JobReconciler{
		client:                       client,
		record:                       record,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		waitForPodsReady:             options.WaitForPodsReady,
		labelKeysToCopy:              options.LabelKeysToCopy,
		clock:                        options.Clock,
		workloadRetentionPolicy:      options.WorkloadRetentionPolicy,
		roleTracker:                  options.RoleTracker,
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
		if jws.Skip(ctx) {
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
			if err := r.client.List(ctx, workloads, client.InNamespace(req.Namespace), OwnerReferenceIndexFieldMatcher(job.GVK(), req.Name)); err != nil {
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

	if features.Enabled(features.ManagedJobsNamespaceSelectorAlwaysRespected) {
		ns := corev1.Namespace{}
		if err := r.client.Get(ctx, client.ObjectKey{Name: req.Namespace}, &ns); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(2).Info("Namespace not found; skipping selector check", "namespace", req.Namespace)
				return ctrl.Result{}, nil
			}
			log.Error(err, "failed to get namespace for selector check")
			return ctrl.Result{}, err
		}

		if r.managedJobsNamespaceSelector == nil {
			log.V(2).Info("ManagedJobsNamespaceSelector is nil; skipping selector enforcement")
		} else if !r.managedJobsNamespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			log.V(2).Info("Namespace not opted in for Kueue management", "namespace", ns.Name)
			return ctrl.Result{}, nil
		}
	}

	var (
		ancestorJob   client.Object
		isTopLevelJob bool
	)

	if topLevelJob, ok := job.(TopLevelJob); ok && topLevelJob.IsTopLevel() {
		// Skipping traversal to top-level ancestor job because this is already a top-level job.
		isTopLevelJob = true
	} else {
		ancestorJob, err = FindAncestorJobManagedByKueue(ctx, r.client, object, r.manageJobsWithoutQueueName)
		if err != nil {
			if errors.Is(err, ErrManagedOwnersChainLimitReached) {
				errMsg := fmt.Sprintf("Terminated search for Kueue-managed Job because ancestor depth exceeded limit of %d", managedOwnersChainLimit)
				r.record.Eventf(object, corev1.EventTypeWarning, ReasonJobNestingTooDeep, errMsg)
				log.Error(err, errMsg)
			}
			return ctrl.Result{}, err
		}
		isTopLevelJob = ancestorJob == nil
	}

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that either
	// have a queue-name label or have a kueue-managed ancestor that has a queue-name label.
	if !r.manageJobsWithoutQueueName && QueueName(job) == "" {
		if isTopLevelJob {
			log.V(3).Info("queue-name label is not set, ignoring the job")
			return ctrl.Result{}, nil
		}
		if QueueNameForObject(ancestorJob) == "" {
			log.V(3).Info("No kueue-managed ancestors have a queue-name label, ignoring the job")
			return ctrl.Result{}, nil
		}
	}

	// when manageJobsWithoutQueueName is enabled, standalone jobs without queue names
	// are still not managed if they don't match the namespace selector.
	if r.manageJobsWithoutQueueName && QueueName(job) == "" {
		ns := corev1.Namespace{}
		err := r.client.Get(ctx, client.ObjectKey{Name: job.Object().GetNamespace()}, &ns)
		if err != nil {
			log.Error(err, "failed to get job namespace")
			return ctrl.Result{}, err
		}
		if !r.managedJobsNamespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			log.V(3).Info("namespace selector does not match, ignoring the job", "namespace", ns.Name)
			return ctrl.Result{}, nil
		}
	}

	// if this is a non-toplevel job, suspend the job if its ancestor's workload is not found or not admitted.
	if !isTopLevelJob {
		if shouldSuspend, err := r.shouldSuspendChildJob(ctx, job, ancestorJob); err != nil {
			return ctrl.Result{}, err
		} else if shouldSuspend {
			if err := clientutil.Patch(ctx, r.client, object, func() (bool, error) {
				job.Suspend()
				return true, nil
			}); err != nil {
				log.Error(err, "suspending child job failed")
				return ctrl.Result{}, err
			}
			r.record.Event(object, corev1.EventTypeNormal, ReasonSuspended, "Kueue managed child job suspended")
		}
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling Job")

	// 1. Attempt to retrieve an existing workload (if any) for this job.
	wl, err := r.ensureOneWorkload(ctx, job, object)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update workload conditions if implemented JobWithCustomWorkloadConditions interface.
	if jobCond, ok := job.(JobWithCustomWorkloadConditions); wl != nil && ok {
		if conditions, updated := jobCond.CustomWorkloadConditions(wl); updated {
			return reconcile.Result{}, workload.PatchStatus(
				ctx, r.client, wl,
				client.FieldOwner(fmt.Sprintf("%s-%s-controller", constants.KueueName, strings.ToLower(job.GVK().Kind))),
				func(wl *kueue.Workload) (bool, error) {
					for _, cond := range conditions {
						apimeta.SetStatusCondition(&wl.Status.Conditions, cond)
					}
					return true, nil
				})
		}
	}

	if jobact, ok := job.(JobWithCustomWorkloadActivation); wl != nil && ok {
		active := jobact.IsWorkloadActive()
		if workload.IsActive(wl) != active {
			wl.Spec.Active = ptr.To(active)
			if err := r.client.Update(ctx, wl); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if wl != nil && workload.IsFinished(wl) {
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
	if message, success, finished := job.Finished(ctx); finished {
		log.V(3).Info("The workload is already finished")
		if wl != nil && !workload.IsFinished(wl) {
			reason := kueue.WorkloadFinishedReasonSucceeded
			if !success {
				reason = kueue.WorkloadFinishedReasonFailed
			}
			err := workload.Finish(ctx, r.client, wl, reason, message, r.clock, r.roleTracker)
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
			if apierrors.IsAlreadyExists(err) {
				log.V(3).Info("Handling job with no workload found an existing workload")
				return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
			}
			if IsUnretryableError(err) {
				log.V(3).Info("Handling job with no workload", "unretryableError", err)
			} else {
				log.Error(err, "Handling job with no workload")
			}
		}
		return ctrl.Result{}, err
	}

	// 4. update reclaimable counts if implemented by the job
	if jobRecl, implementsReclaimable := job.(JobWithReclaimablePods); implementsReclaimable && features.Enabled(features.ReclaimablePods) {
		reclPods, err := jobRecl.ReclaimablePods(ctx)
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
			log.V(3).Info("updated reclaimable pods")
			return ctrl.Result{}, nil
		}
		log.V(3).Info("reclaimable pods are up-to-date")
	}

	// 5. handle WaitForPodsReady only for a standalone job.
	// handle a job when waitForPodsReady is enabled, and it is the main job
	if r.waitForPodsReady {
		log.V(3).Info("Handling a job when waitForPodsReady is enabled")
		condition := generatePodsReadyCondition(ctx, job, wl, r.clock)
		if !workload.HasConditionWithTypeAndReason(wl, &condition) {
			log.V(3).Info("Updating the PodsReady condition", "reason", condition.Reason, "status", condition.Status)
			err := workload.SetConditionAndUpdate(ctx, r.client, wl, condition.Type, condition.Status, condition.Reason, condition.Message, constants.JobControllerName, r.clock)
			if err != nil {
				log.Error(err, "Updating workload status")
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			// update the metrics only when PodsReady condition status is true
			if condition.Status == metav1.ConditionTrue {
				cqName := wl.Status.Admission.ClusterQueue
				priorityClassName := workload.PriorityClassName(wl)
				queuedUntilReadyWaitTime := workload.QueuedWaitTime(wl, r.clock)
				metrics.ReadyWaitTime(cqName, priorityClassName, queuedUntilReadyWaitTime, r.roleTracker)
				admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
				admittedUntilReadyWaitTime := condition.LastTransitionTime.Sub(admittedCond.LastTransitionTime.Time)
				metrics.ReportAdmittedUntilReadyWaitTime(cqName, priorityClassName, admittedUntilReadyWaitTime, r.roleTracker)
				if features.Enabled(features.LocalQueueMetrics) {
					metrics.LocalQueueReadyWaitTime(metrics.LQRefFromWorkload(wl), priorityClassName, queuedUntilReadyWaitTime, r.roleTracker)
					metrics.ReportLocalQueueAdmittedUntilReadyWaitTime(metrics.LQRefFromWorkload(wl), priorityClassName, admittedUntilReadyWaitTime, r.roleTracker)
				}
			}
			return ctrl.Result{}, nil
		} else {
			log.V(3).Info("No update for PodsReady condition")
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
				err := workload.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
					// The requeued condition status set to true only on EvictedByPreemption
					setRequeued := (evCond.Reason == kueue.WorkloadEvictedByPreemption) || (evCond.Reason == kueue.WorkloadEvictedDueToNodeFailures)
					updated := workload.SetRequeuedCondition(wl, evCond.Reason, evCond.Message, setRequeued)
					if workload.UnsetQuotaReservationWithCondition(wl, "Pending", evCond.Message, r.clock.Now()) {
						updated = true
					}
					return updated, nil
				})
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("clearing admission: %w", err)
				}
			}
		}
		requeueAfter, err := r.handleWorkloadAfterDeactivatedPolicy(ctx, job, wl)
		return ctrl.Result{RequeueAfter: requeueAfter}, err
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
					errUpdateStatus := workload.Finish(ctx, r.client, wl, FailedToStartFinishedReason, err.Error(), r.clock, r.roleTracker)
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
		// The job must be suspended if the workload is not yet admitted,
		// unless this job is workload-slicing enabled. In workload-slicing we rely
		// on pod-scheduling gate(s) to pause workload slice pods during the workload admission process.
		if WorkloadSliceEnabled(job) {
			return ctrl.Result{}, nil
		}
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		err := r.stopJob(ctx, job, wl, StopReasonNotAdmitted, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	if WorkloadSliceEnabled(job) {
		// Start workload-slice schedule-gated pods (if any).
		log.V(3).Info("Job running with admitted workload slice, start pods.")
		return ctrl.Result{}, workloadslicing.StartWorkloadSlicePods(ctx, r.client, wl)
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

func (r *JobReconciler) shouldSuspendChildJob(ctx context.Context, childJob GenericJob, ancestorJob client.Object) (bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("childJob", childJob.Object().GetName(), "gvk", childJob.GVK(), "ancestorJob", ancestorJob.GetName())
	_, _, finished := childJob.Finished(ctx)
	if !finished && !childJob.IsSuspended() {
		if ancestorNotFinishedWorkload, err := r.getLatestNotFinishedWorkloadForObject(ctx, ancestorJob); err != nil {
			log.Error(err, "couldn't get an ancestor job not-finished workload")
			return false, err
		} else {
			if ancestorNotFinishedWorkload == nil {
				return true, nil
			}
			if workloadslicing.Enabled(childJob.Object()) || workloadslicing.Enabled(ancestorJob) {
				// With workload slicing, during autoscaling-up, there will be two workloads at certain time for workload slice
				// replacement. The old one was finished, the new one is going to be admitted. There is no admitted workload in
				// this case, and we should not suspend the job. As a workaround, check evicted status instead.
				return workload.IsEvicted(ancestorNotFinishedWorkload), nil
			}
			// For none workload slicing, suspend the job if workload not admitted
			return !workload.IsAdmitted(ancestorNotFinishedWorkload), nil
		}
	}
	return false, nil
}

func (r *JobReconciler) shouldHandleDeletionOfDeactivatedWorkload(wl *kueue.Workload) bool {
	return r.workloadRetentionPolicy.AfterDeactivatedByKueue != nil && !workload.IsActive(wl) && workload.IsEvictedDueToDeactivationByKueue(wl)
}

func (r *JobReconciler) handleWorkloadAfterDeactivatedPolicy(ctx context.Context, job GenericJob, wl *kueue.Workload) (time.Duration, error) {
	if !r.shouldHandleDeletionOfDeactivatedWorkload(wl) {
		return 0, nil
	}

	log := ctrl.LoggerFrom(ctx)
	evCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
	requeueAfter := evCond.LastTransitionTime.Add(*r.workloadRetentionPolicy.AfterDeactivatedByKueue).Sub(r.clock.Now())

	if requeueAfter <= 0 {
		object := job.Object()
		log.V(2).Info(
			"Deleting job: deactivation retention period expired",
			"retention", *r.workloadRetentionPolicy.AfterDeactivatedByKueue,
		)
		if err := r.client.Delete(ctx, object, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			return 0, client.IgnoreNotFound(err)
		}
		r.record.Event(object, corev1.EventTypeNormal, ReasonDeleted,
			"Deleted job: deactivation retention period expired")
		if err := r.finalizeJob(ctx, job); err != nil {
			return 0, err
		}
		return 0, nil
	}

	log.V(3).Info("Requeuing job for deletion", "requeueAfter", requeueAfter)
	return requeueAfter, nil
}

func (r *JobReconciler) recordAdmissionCheckUpdate(wl *kueue.Workload, job GenericJob) {
	message := ""
	object := job.Object()
	for _, check := range wl.Status.AdmissionChecks {
		if check.State == kueue.CheckStatePending && check.Message != "" {
			if message != "" {
				message += "; "
			}
			message += string(check.Name) + ": " + check.Message
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

// getLatestNotFinishedWorkloadForObject returns the latest not-finished Workload associated with the given job.
// Returns nil if no such workload is found.
func (r *JobReconciler) getLatestNotFinishedWorkloadForObject(ctx context.Context, jobObj client.Object) (*kueue.Workload, error) {
	gvk, err := apiutil.GVKForObject(jobObj, r.client.Scheme())
	if err != nil {
		return nil, err
	}
	workloads, err := workloadslicing.FindNotFinishedWorkloads(ctx, r.client, jobObj, gvk)
	if err != nil {
		return nil, err
	}

	if len(workloads) == 0 {
		return nil, nil
	}

	return &workloads[len(workloads)-1], nil
}

// FindAncestorJobManagedByKueue traverses controllerRefs to find the top-level ancestor Job managed by Kueue.
// If manageJobsWithoutQueueName is set to false, it returns only Jobs with a queue-name.
// If manageJobsWithoutQueueName is true, it may return a Job even if it doesn't have a queue-name.
//
// Examples:
//
// With manageJobsWithoutQueueName=false:
// Job -> JobSet -> AppWrapper => nil
// Job (queue-name) -> JobSet (queue-name) -> AppWrapper => JobSet
// Job (queue-name) -> JobSet -> AppWrapper (queue-name) => AppWrapper
// Job (queue-name) -> JobSet (queue-name) -> AppWrapper (queue-name) => AppWrapper
// Job -> JobSet (disabled) -> AppWrapper (queue-name) => AppWrapper
//
// With manageJobsWithoutQueueName=true:
// Job -> JobSet -> AppWrapper => AppWrapper
// Job (queue-name) -> JobSet (queue-name) -> AppWrapper => AppWrapper
// Job (queue-name) -> JobSet -> AppWrapper (queue-name) => AppWrapper
// Job (queue-name) -> JobSet (queue-name) -> AppWrapper (queue-name) => AppWrapper
// Job -> JobSet (disabled) -> AppWrapper => AppWrapper
func FindAncestorJobManagedByKueue(ctx context.Context, c client.Client, jobObj client.Object, manageJobsWithoutQueueName bool) (client.Object, error) {
	log := ctrl.LoggerFrom(ctx)
	seen := sets.New[types.UID]()
	currentObj := jobObj

	var topLevelJob client.Object
	for {
		if seen.Has(currentObj.GetUID()) {
			log.Error(ErrCyclicOwnership,
				"Terminated search for Kueue-managed Job because of cyclic ownership",
				"currentObj", currentObj,
			)
			return nil, ErrCyclicOwnership
		}
		seen.Insert(currentObj.GetUID())

		owner := metav1.GetControllerOf(currentObj)
		if owner == nil {
			log.V(3).Info("stop walking up as the owner is not found", "currentObj", klog.KObj(currentObj))
			return topLevelJob, nil
		}

		if !manager.isKnownOwner(owner) {
			log.V(3).Info(
				"stop walking up as the owner is not known",
				"currentObj", klog.KObj(currentObj),
				"owner", klog.KRef(jobObj.GetNamespace(), owner.Name),
			)
			return topLevelJob, nil
		}
		parentObj := GetEmptyOwnerObject(owner)
		managed := parentObj != nil
		if parentObj == nil {
			parentObj = &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{
					APIVersion: owner.APIVersion,
					Kind:       owner.Kind,
				},
			}
		}
		if err := c.Get(ctx, client.ObjectKey{Name: owner.Name, Namespace: jobObj.GetNamespace()}, parentObj); err != nil {
			return nil, errors.Join(ErrWorkloadOwnerNotFound, err)
		}
		if managed && (manageJobsWithoutQueueName || QueueNameForObject(parentObj) != "") {
			topLevelJob = parentObj
		}
		currentObj = parentObj
		if len(seen) > managedOwnersChainLimit {
			return nil, ErrManagedOwnersChainLimitReached
		}
	}
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
		// Ignore the workload is controlled by another object.
		if controlledBy := metav1.GetControllerOfNoCopy(wl); controlledBy != nil && controlledBy.UID != object.GetUID() {
			log.V(2).Info(
				"WARNING: The workload is already controlled by another object",
				"workload", klog.KObj(wl),
				"controlledBy", controlledBy,
			)
			return nil, nil
		}

		if cj, implements := job.(ComposableJob); implements {
			err = cj.EnsureWorkloadOwnedByAllMembers(ctx, r.client, r.record, wl)
		} else {
			err = EnsurePrebuiltWorkloadOwnership(ctx, r.client, wl, object)
		}
		if err != nil {
			return nil, err
		}

		// Skip the in-sync check for ElasticJob workloads if the workload is a
		// newly scaled-up replacement. This prevents premature removal of remote
		// objects for a Job that has not yet been synced after scale-up.
		if workloadslicing.Enabled(object) && workloadslicing.ScaledUp(wl) {
			log.V(3).Info("WorkloadSlice: skip in-sync check in ensurePrebuiltWorkload")
			return wl, nil
		}

		if inSync, err := r.ensurePrebuiltWorkloadInSync(ctx, wl, job); !inSync || err != nil {
			return nil, err
		}
		return wl, nil
	}

	// If workload slicing is enabled for this job, use the slice-based processing path.
	if WorkloadSliceEnabled(job) {
		podSets, err := JobPodSets(ctx, job)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve pod sets from job: %w", err)
		}

		// Workload slices allow modifications only to PodSet.Count.
		// Any other changes will result in the slice being marked as incompatible,
		// and the workload will fall back to being processed by the original ensureOneWorkload function.
		wl, compatible, err := workloadslicing.EnsureWorkloadSlices(ctx, r.client, r.clock, podSets, object, job.GVK(), r.roleTracker)
		if err != nil {
			return nil, err
		}
		if compatible {
			return wl, nil
		}
		// Fallback.
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

		if _, _, finished := job.Finished(ctx); !finished {
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
			return nil, fmt.Errorf("failed to remove workload finalizer for: %w", err)
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

	if match != nil {
		if err := UpdateWorkloadPriority(ctx, r.client, r.record, job.Object(), match, getCustomPriorityClassFuncFromJob(job)); err != nil {
			return nil, err
		}
	}

	return match, nil
}

// UpdateWorkloadPriority updates workload priority if object's kueue.x-k8s.io/priority-class label changed.
func UpdateWorkloadPriority(ctx context.Context, c client.Client, r record.EventRecorder, obj client.Object, wl *kueue.Workload, customPriorityClassFunc func() string) error {
	jobPriorityClassName := WorkloadPriorityClassName(obj)
	wlPriorityClassName := workload.PriorityClassName(wl)

	// This handles both: changing priority (old -> new) AND adding priority (none -> new)
	if (workload.HasNoPriority(wl) || workload.IsWorkloadPriorityClass(wl)) && jobPriorityClassName != wlPriorityClassName {
		if err := PrepareWorkloadPriority(ctx, c, obj, wl, customPriorityClassFunc); err != nil {
			return fmt.Errorf("prepare workload priority: %w", err)
		}
		if err := c.Update(ctx, wl); err != nil {
			return fmt.Errorf("updating existing workload: %w", err)
		}
		r.Eventf(obj,
			corev1.EventTypeNormal, ReasonUpdatedWorkload,
			"Updated workload priority class: %v", klog.KObj(wl),
		)
	}
	return nil
}

func FindMatchingWorkloads(ctx context.Context, c client.Client, job GenericJob) (match *kueue.Workload, toDelete []*kueue.Workload, err error) {
	object := job.Object()

	workloads := &kueue.WorkloadList{}
	if err := c.List(ctx, workloads, client.InNamespace(object.GetNamespace()), OwnerReferenceIndexFieldMatcher(job.GVK(), object.GetName())); err != nil {
		return nil, nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		isEquivalent, err := EquivalentToWorkload(ctx, c, job, w)
		if err != nil {
			return nil, nil, err
		}
		if match == nil && isEquivalent {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	return match, toDelete, nil
}

func EnsurePrebuiltWorkloadOwnership(ctx context.Context, c client.Client, wl *kueue.Workload, object client.Object) error {
	if !metav1.IsControlledBy(wl, object) {
		if err := ctrl.SetControllerReference(object, wl, c.Scheme()); err != nil {
			return err
		}

		if errs := validation.IsValidLabelValue(string(object.GetUID())); len(errs) == 0 {
			if wl.Labels == nil {
				wl.Labels = make(map[string]string, 1)
			}
			wl.Labels[controllerconsts.JobUIDLabel] = string(object.GetUID())
		}

		if err := c.Update(ctx, wl); err != nil {
			return err
		}
	}
	return nil
}

func (r *JobReconciler) ensurePrebuiltWorkloadInSync(ctx context.Context, wl *kueue.Workload, job GenericJob) (bool, error) {
	var (
		equivalent bool
		err        error
	)

	if cj, implements := job.(ComposableJob); implements {
		equivalent, err = cj.EquivalentToWorkload(ctx, r.client, wl)
	} else {
		equivalent, err = EquivalentToWorkload(ctx, r.client, job, wl)
	}

	if !equivalent || err != nil {
		if err != nil {
			return false, err
		}
		// mark the workload as finished
		msg := "The prebuilt workload is out of sync with its user job"
		return false, workload.Finish(ctx, r.client, wl, kueue.WorkloadFinishedReasonOutOfSync, msg, r.clock, r.roleTracker)
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
	infoMap := slices.ToRefMap(info, func(psi *podset.PodSetInfo) kueue.PodSetReference { return psi.Name })
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

// EquivalentToWorkload checks if the job corresponds to the workload
func EquivalentToWorkload(ctx context.Context, c client.Client, job GenericJob, wl *kueue.Workload) (bool, error) {
	owner := metav1.GetControllerOf(wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != job.Object().GetName() {
		return false, nil
	}

	defaultDuration := int32(-1)
	if ptr.Deref(wl.Spec.MaximumExecutionTimeSeconds, defaultDuration) != ptr.Deref(MaximumExecutionTimeSeconds(job), defaultDuration) {
		return false, nil
	}

	getPodSets, err := JobPodSets(ctx, job)
	if err != nil {
		return false, err
	}
	jobPodSets := clearMinCountsIfFeatureDisabled(getPodSets)

	if runningPodSets := expectedRunningPodSets(ctx, c, wl); runningPodSets != nil {
		if equality.ComparePodSetSlices(jobPodSets, runningPodSets, workload.IsAdmitted(wl)) {
			return true, nil
		}
		// If the workload is admitted but the job is suspended, do the check
		// against the non-running info.
		// This might allow some violating jobs to pass equivalency checks, but their
		// workloads would be invalidated in the next sync after unsuspending.
		return job.IsSuspended() && equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, workload.IsAdmitted(wl)), nil
	}

	return equality.ComparePodSetSlices(jobPodSets, wl.Spec.PodSets, workload.IsAdmitted(wl)), nil
}

func (r *JobReconciler) updateWorkloadToMatchJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) (*kueue.Workload, error) {
	newWl, err := r.constructWorkload(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("can't construct workload for update: %w", err)
	}
	err = r.prepareWorkload(ctx, job, newWl, wl.Spec.Active)
	if err != nil {
		return nil, fmt.Errorf("can't construct workload for update: %w", err)
	}
	wl.Spec = newWl.Spec
	if err = r.client.Update(ctx, wl); err != nil {
		return nil, fmt.Errorf("updating existed workload: %w", err)
	}

	r.record.Eventf(object, corev1.EventTypeNormal, ReasonUpdatedWorkload,
		"Updated not matching Workload for suspended job: %v", klog.KObj(wl))
	return wl, nil
}

// startJob will unsuspend the job, and also inject the node affinity.
func (r *JobReconciler) startJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) error {
	info, err := getPodSetsInfoFromStatus(ctx, r.client, wl)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Admitted by clusterQueue %v", wl.Status.Admission.ClusterQueue)

	log := ctrl.LoggerFrom(ctx)
	if features.Enabled(features.MultiKueue) {
		_, isComposable := job.(ComposableJob)

		if isComposable {
			skip, err := admissioncheck.ShouldSkipLocalExecution(ctx, r.client, wl)
			if err != nil {
				log.V(3).Info("Failed to check for MultiKueue admission check", "workload", klog.KObj(wl), "error", err)
				return err
			}
			if skip {
				log.V(3).Info("Workload has MultiKueue admission check, skipping local execution", "workload", klog.KObj(wl))
				return nil
			}
		}
	}

	if cj, implements := job.(ComposableJob); implements {
		if err := cj.Run(ctx, r.client, info, r.record, msg); err != nil {
			return err
		}
	} else {
		if err := clientutil.Patch(ctx, r.client, object, func() (bool, error) {
			return true, job.RunWithPodSetsInfo(ctx, info)
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

	if err := clientutil.Patch(ctx, r.client, object, func() (bool, error) {
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
func (r *JobReconciler) constructWorkload(ctx context.Context, job GenericJob) (*kueue.Workload, error) {
	if cj, implements := job.(ComposableJob); implements {
		wl, err := cj.ConstructComposableWorkload(ctx, r.client, r.record, r.labelKeysToCopy)
		if err != nil {
			return nil, err
		}
		return wl, nil
	}
	return ConstructWorkload(ctx, r.client, job, r.labelKeysToCopy)
}

// newWorkloadName generates a new workload name for the given job, incorporating the job's name, UID,
// and GroupVersionKind (GVK). If workload slicing is enabled, it includes the job's generation
// in the generated workload name.
func newWorkloadName(job GenericJob) string {
	object := job.Object()
	if WorkloadSliceEnabled(job) {
		return GetWorkloadNameForOwnerWithGVKAndGeneration(object.GetName(), object.GetUID(), job.GVK(), object.GetGeneration())
	}
	return GetWorkloadNameForOwnerWithGVK(object.GetName(), object.GetUID(), job.GVK())
}

func ConstructWorkload(ctx context.Context, c client.Client, job GenericJob, labelKeysToCopy []string) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)
	object := job.Object()

	podSets, err := JobPodSets(ctx, job)
	if err != nil {
		return nil, err
	}

	wl := NewWorkload(newWorkloadName(job), object, podSets, labelKeysToCopy)
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

	if err := ctrl.SetControllerReference(object, wl, c.Scheme()); err != nil {
		return nil, err
	}

	return wl, nil
}

// prepareWorkloadSlice adds necessary workload slice annotations.
func prepareWorkloadSlice(ctx context.Context, clnt client.Client, job GenericJob, wl *kueue.Workload) error {
	// Annotate the workload to indicate that elastic-job support is enabled.
	// This annotation makes it possible to distinguish workloads with elastic-job
	// support directly at the workload level, without requiring access to the
	// associated Job object. This is particularly useful in contexts such as the
	// MultiKueue workload controller, where the Job may not be immediately available.
	metav1.SetMetaDataAnnotation(&wl.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)

	// Lookup existing slice for a given job.
	workloadSlices, err := workloadslicing.FindNotFinishedWorkloads(ctx, clnt, job.Object(), job.GVK())
	if err != nil {
		return fmt.Errorf("failure looking up workload slices: %w", err)
	}

	switch len(workloadSlices) {
	case 0:
		// First workload - set origin name to itself.
		metav1.SetMetaDataAnnotation(&wl.ObjectMeta, kueue.WorkloadSliceNameAnnotation, wl.Name)
		return nil
	case 1:
		// Scale-up event - link to old slice and carry origin name.
		oldSlice := workloadSlices[0]
		metav1.SetMetaDataAnnotation(&wl.ObjectMeta, workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(&oldSlice)))
		originName := oldSlice.Annotations[kueue.WorkloadSliceNameAnnotation]
		if originName == "" {
			originName = oldSlice.Name
		}
		metav1.SetMetaDataAnnotation(&wl.ObjectMeta, kueue.WorkloadSliceNameAnnotation, originName)
		return nil
	default:
		// Any other slices length is invalid. I.E, we expect to have at most 1 "current/old" workload slice.
		// Failing here, would trigger job re-processing, and hopefully giving a chance to clear up (preempt/deactivate)
		// old slice.
		return fmt.Errorf("unexpected workload-slices count: %d", len(workloadSlices))
	}
}

func getCustomPriorityClassFuncFromJob(job GenericJob) func() string {
	if jobWithPriorityClass, isImplemented := job.(JobWithPriorityClass); isImplemented {
		return jobWithPriorityClass.PriorityClass
	}
	return nil
}

func PrepareWorkloadPriority(ctx context.Context, c client.Client, obj client.Object, wl *kueue.Workload, customPriorityClassFunc func() string) error {
	priorityClassRef, priority, err := ExtractPriority(ctx, c, obj, wl.Spec.PodSets, customPriorityClassFunc)
	if err != nil {
		return err
	}

	wl.Spec.PriorityClassRef = priorityClassRef
	wl.Spec.Priority = &priority

	return nil
}

// prepareWorkload adds the priority information for the constructed workload
// active is used to set the active field of the workload. If active is nil, the workload will be set to active by default.
// for the existing workload, the original active status should be retained.
func (r *JobReconciler) prepareWorkload(ctx context.Context, job GenericJob, wl *kueue.Workload, active *bool) error {
	if err := PrepareWorkloadPriority(ctx, r.client, job.Object(), wl, getCustomPriorityClassFuncFromJob(job)); err != nil {
		return err
	}

	wl.Spec.PodSets = clearMinCountsIfFeatureDisabled(wl.Spec.PodSets)

	if WorkloadSliceEnabled(job) {
		return prepareWorkloadSlice(ctx, r.client, job, wl)
	}
	wl.Spec.Active = active
	return nil
}

func ExtractPriority(ctx context.Context, c client.Client, obj client.Object, podSets []kueue.PodSet, customPriorityClassFunc func() string) (*kueue.PriorityClassRef, int32, error) {
	if workloadPriorityClass := WorkloadPriorityClassName(obj); len(workloadPriorityClass) > 0 {
		return utilpriority.GetPriorityFromWorkloadPriorityClass(ctx, c, workloadPriorityClass)
	}
	if customPriorityClassFunc != nil {
		return utilpriority.GetPriorityFromPriorityClass(ctx, c, customPriorityClassFunc())
	}
	return utilpriority.GetPriorityFromPriorityClass(ctx, c, extractPriorityFromPodSets(podSets))
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

	for i, psAssignment := range w.Status.Admission.PodSetAssignments {
		info, err := podset.FromAssignment(ctx, c, &psAssignment, &w.Spec.PodSets[i])
		if err != nil {
			return nil, err
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			info.Annotations[kueue.WorkloadAnnotation] = w.Name
		}
		// Set workload slice name annotation on pods for elastic workloads.
		// This enables pod lookup by annotation instead of owner reference,
		// supporting JobSet and other workloads where pods are not immediate children.
		if features.Enabled(features.ElasticJobsViaWorkloadSlices) {
			info.Annotations[kueue.WorkloadSliceNameAnnotation] = workloadslicing.SliceName(w)
		}

		info.Labels[constants.PodSetLabel] = string(psAssignment.Name)

		if features.Enabled(features.AssignQueueLabelsForPods) {
			assignQueueLabels(ctx, info.Labels, w)
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

func assignQueueLabels(ctx context.Context, labels map[string]string, wl *kueue.Workload) {
	labels[constants.LocalQueueLabel] = string(wl.Spec.QueueName)

	clusterQueueName := string(wl.Status.Admission.ClusterQueue)
	labelValidationErrors := validation.IsDNS1123Label(clusterQueueName)
	if len(labelValidationErrors) == 0 {
		labels[constants.ClusterQueueLabel] = clusterQueueName
	} else {
		log := ctrl.LoggerFrom(ctx)
		log.V(2).Info("Cluster queue name could not be set as a label for pods",
			"queue name", clusterQueueName, "validation errors", labelValidationErrors)
	}
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

	// Wait until there are no active pods, unless this is a workload-slice job.
	// For workload-slice enabled jobs, we allow the job to remain "Active" to accommodate
	// the scale-up case, where the new workload slice replaces the old workload slice.
	if job.IsActive() && !WorkloadSliceEnabled(job) {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	if usePrebuiltWorkload {
		return ErrPrebuiltWorkloadNotFound
	}

	// Create the corresponding workload.
	wl, err := r.constructWorkload(ctx, job)
	if err != nil {
		return err
	}
	err = r.prepareWorkload(ctx, job, wl, wl.Spec.Active)
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

func generatePodsReadyCondition(ctx context.Context, job GenericJob, wl *kueue.Workload, clock clock.Clock) metav1.Condition {
	log := ctrl.LoggerFrom(ctx)
	const (
		notReadyMsg           = "Not all pods are ready or succeeded"
		waitingForRecoveryMsg = "At least one pod has failed, waiting for recovery"
		readyMsg              = "All pods reached readiness and the workload is running"
	)
	if !workload.IsAdmitted(wl) {
		// The workload has not been admitted yet
		// or it was admitted in the past but it's evicted/requeued
		return workload.CreatePodsReadyCondition(metav1.ConditionFalse,
			kueue.WorkloadWaitForStart,
			notReadyMsg,
			clock)
	}

	podsReadyCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
	podsReady := job.PodsReady(ctx)
	log.V(3).Info("Generating PodsReady condition",
		"Current PodsReady condition", podsReadyCond,
		"Pods are ready", podsReady)

	if podsReady {
		reason := kueue.WorkloadStarted
		if podsReadyCond != nil && (podsReadyCond.Reason == kueue.WorkloadWaitForRecovery || podsReadyCond.Reason == kueue.WorkloadRecovered) {
			reason = kueue.WorkloadRecovered
		}
		return workload.CreatePodsReadyCondition(metav1.ConditionTrue,
			reason,
			readyMsg,
			clock)
	}

	switch {
	case podsReadyCond == nil:
		return workload.CreatePodsReadyCondition(metav1.ConditionFalse,
			kueue.WorkloadWaitForStart,
			notReadyMsg,
			clock)

	case podsReadyCond.Status == metav1.ConditionTrue:
		return workload.CreatePodsReadyCondition(metav1.ConditionFalse,
			kueue.WorkloadWaitForRecovery,
			waitingForRecoveryMsg,
			clock)

	case podsReadyCond.Reason == kueue.WorkloadWaitForRecovery:
		return workload.CreatePodsReadyCondition(metav1.ConditionFalse,
			kueue.WorkloadWaitForRecovery,
			waitingForRecoveryMsg,
			clock)

	default:
		// handles both "WaitForPodsStart" and the old "PodsReady" reasons
		return workload.CreatePodsReadyCondition(metav1.ConditionFalse,
			kueue.WorkloadWaitForStart,
			notReadyMsg,
			clock)
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
	return func(ctx context.Context, client client.Client, indexer client.FieldIndexer, record record.EventRecorder, opts ...Option) (JobReconcilerInterface, error) {
		return &genericReconciler{
			jr:     NewReconciler(client, record, opts...),
			newJob: newJob,
			setup:  setup,
		}, nil
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
	controllerName := strings.ToLower(r.newJob().GVK().Kind)
	b := ctrl.NewControllerManagedBy(mgr).
		For(r.newJob().Object()).Owns(&kueue.Workload{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.jr.RoleTracker(), controllerName),
		})
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

// WorkloadSliceEnabled returns true if all the following conditions are met:
//   - The ElasticJobsViaWorkloadSlices feature is enabled.
//   - The provided job is not nil.
//   - The job's underlying object is not nil.
//   - The job's object has opted in for WorkloadSlice processing.
func WorkloadSliceEnabled(job GenericJob) bool {
	if job == nil {
		return false
	}
	jobObject := job.Object()
	if jobObject == nil {
		return false
	}
	return workloadslicing.Enabled(jobObject)
}
