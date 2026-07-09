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

package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	gocmp "github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	qutil "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	realClock = clock.RealClock{}

	// schedulerSetReasons contains the reasons for the QuotaReserved=False condition
	// that are set by the scheduler and must be preserved by the controller during
	// reconciliation until the next scheduler cycle.
	//
	// We explicitly DO NOT include:
	// - WorkloadQuotaReservedReasonSuspended
	// - WorkloadQuotaReservedReasonMisconfigured
	// Even though the scheduler can emit them, they represent active blockages (e.g.
	// inactive or missing queues) that the controller actively re-evaluates. If we
	// preserved them, the controller would not be able to transition the workload
	// back to PendingEvaluation once the blockages are resolved.
	schedulerSetReasons = sets.New(
		kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
		kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
		kueue.WorkloadQuotaReservedReasonWaitingForPodsReady,
		kueue.WorkloadQuotaReservedReasonPendingEvaluation,
	)
)

// hasInternalError reports whether the field error list contains an internal
// error. DRA resolution reports retryable failures — API errors and cluster-state
// shortages that clear once ResourceSlices change — as internal errors, so the
// reconciler retries them with controller-runtime backoff; deterministic spec or
// configuration errors use other field error types and are left inadmissible
// without requeue.
func hasInternalError(errs field.ErrorList) bool {
	for _, e := range errs {
		if e.Type == field.ErrorTypeInternal {
			return true
		}
	}
	return false
}

type waitForPodsReadyConfig struct {
	timeout                     time.Duration
	recoveryTimeout             *time.Duration
	requeuingBackoffLimitCount  *int32
	requeuingBackoffBaseSeconds int32
	requeuingBackoffMaxDuration time.Duration
	requeuingBackoffJitter      float64
}

type workloadRetentionConfig struct {
	afterFinished *time.Duration
}

// Option configures the reconciler.
type Option func(*WorkloadReconciler)

// WithWaitForPodsReady indicates the configuration for the WaitForPodsReady feature.
func WithWaitForPodsReady(value *waitForPodsReadyConfig) Option {
	return func(r *WorkloadReconciler) {
		r.waitForPodsReady = value
	}
}

// WithWorkloadUpdateWatchers allows to specify the workload update watchers
func WithWorkloadUpdateWatchers(value ...WorkloadUpdateWatcher) Option {
	return func(r *WorkloadReconciler) {
		r.watchers = value
	}
}

// WithWorkloadRetention allows to specify retention for workload resources
func WithWorkloadRetention(value *workloadRetentionConfig) Option {
	return func(r *WorkloadReconciler) {
		r.workloadRetention = value
	}
}

// WithWorkloadRoleTracker sets the role tracker for HA setups.
func WithWorkloadRoleTracker(value *roletracker.RoleTracker) Option {
	return func(r *WorkloadReconciler) {
		r.roleTracker = value
	}
}

// WithWorkloadCustomLabels sets the custom labels for workload metrics.
func WithWorkloadCustomLabels(value *metrics.CustomLabels) Option {
	return func(r *WorkloadReconciler) {
		r.customLabels = value
	}
}

// WithPreemptionExpectations sets the store used to track in-flight preemptions.
func WithPreemptionExpectations(value *expectations.Store) Option {
	return func(r *WorkloadReconciler) {
		r.preemptionExpectations = value
	}
}

// WithAdmissionFairSharing sets the admission fair sharing configuration.
func WithAdmissionFairSharing(value *config.AdmissionFairSharing) Option {
	return func(r *WorkloadReconciler) {
		r.admissionFSConfig = value
	}
}

func WithDRAMapper(value *dra.ResourceMapper) Option {
	return func(r *WorkloadReconciler) {
		r.draMapper = value
	}
}

func WithDRABackedResources(value *dra.ExtendedResourceCache) Option {
	return func(r *WorkloadReconciler) {
		r.draBackedResources = value
	}
}

type WorkloadUpdateWatcher interface {
	NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload)
}

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	logName                string
	queues                 *qcache.Manager
	cache                  *schdcache.Cache
	client                 client.Client
	watchers               []WorkloadUpdateWatcher
	waitForPodsReady       *waitForPodsReadyConfig
	recorder               events.EventRecorder
	clock                  clock.Clock
	workloadRetention      *workloadRetentionConfig
	draReconcileChannel    chan event.TypedGenericEvent[*kueue.Workload]
	draMapper              *dra.ResourceMapper
	draBackedResources     *dra.ExtendedResourceCache
	admissionFSConfig      *config.AdmissionFairSharing
	roleTracker            *roletracker.RoleTracker
	preemptionExpectations *expectations.Store
	customLabels           *metrics.CustomLabels
}

var _ reconcile.Reconciler = (*WorkloadReconciler)(nil)
var _ predicate.TypedPredicate[*kueue.Workload] = (*WorkloadReconciler)(nil)

func NewWorkloadReconciler(client client.Client, queues *qcache.Manager, cache *schdcache.Cache, recorder events.EventRecorder, options ...Option) *WorkloadReconciler {
	r := &WorkloadReconciler{
		logName:             "workload-reconciler",
		client:              client,
		queues:              queues,
		cache:               cache,
		recorder:            recorder,
		clock:               realClock,
		draReconcileChannel: make(chan event.TypedGenericEvent[*kueue.Workload], updateChBuffer),
	}
	for _, option := range options {
		option(r)
	}
	return r
}

func (r *WorkloadReconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaimtemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceslices,verbs=get;list;watch

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var wl kueue.Workload
	if err := r.client.Get(ctx, req.NamespacedName, &wl); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")

	if isOrphanedWorkload(&wl) {
		err := workload.FinalizeOrphanedWorkload(ctx, r.client, r.clock, &wl, true)
		if err != nil {
			return ctrl.Result{}, err
		}
		// If it was deleted, there is nothing to do.
		// Otherwise, we still need to handle finished workload logic.
		if !features.Enabled(features.FinishOrphanedWorkloads) || !wl.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, err
		}
	}

	if features.Enabled(features.MultiKueueOrchestratedPreemption) {
		updateErr := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			updated := r.syncPreemptionGateStates(wl)
			return updated, nil
		})
		if updateErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to sync workload preemption gate status: %w", updateErr)
		}
	}

	if features.Enabled(features.ConcurrentAdmission) {
		enabled := r.queues.ConcurrentAdmissionEnabledFor(&wl)
		if enabled && !concurrentadmission.IsVariant(&wl) && !concurrentadmission.IsParent(&wl) {
			// Workload is a Parent without set Parent annotation yet
			concurrentadmission.SetParentVariantLabel(&wl)
			err := r.client.Update(ctx, &wl)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	finishedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished)
	if finishedCond != nil && finishedCond.Status == metav1.ConditionTrue {
		if r.workloadRetention == nil || r.workloadRetention.afterFinished == nil {
			return ctrl.Result{}, nil
		}

		now := r.clock.Now()
		expirationTime := finishedCond.LastTransitionTime.Add(*r.workloadRetention.afterFinished)
		if now.Before(expirationTime) {
			remainingTime := expirationTime.Sub(now)
			log.V(3).Info("Requeueing workload for deletion after retention period", "remainingTime", remainingTime)
			return ctrl.Result{RequeueAfter: remainingTime}, nil
		}

		log.V(2).Info("Deleting workload because it has finished and the retention period has elapsed", "retention", *r.workloadRetention.afterFinished)

		// Finished Workloads should no longer need Kueue's resource-in-use finalizer.
		// However, WorkloadSlices and other paths can mark a Workload as Finished without
		// going through the job finalization path that normally removes it. Remove only
		// Kueue's finalizer before deleting; otherwise Kubernetes may only set
		// deletionTimestamp and the Workload can remain stuck until the finalizer is removed.
		deleteRequested, err := workload.Delete(ctx, r.client, &wl)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if deleteRequested {
			r.recorder.Eventf(&wl, nil, corev1.EventTypeNormal, "Deleted", "Deleted", "Deleted finished workload due to elapsed retention")
		}

		return ctrl.Result{}, nil
	}

	if requeueAt := workload.NeedsRequeueAtUpdate(&wl, r.clock); requeueAt != nil {
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			if wl.Status.RequeueState == nil {
				wl.Status.RequeueState = &kueue.RequeueState{}
			}
			log.V(2).Info("At least one admission check set a retry time", "requeueAt", requeueAt, "current", wl.Status.RequeueState.RequeueAt)
			workload.SetRequeueState(wl, *requeueAt, false)
			return true, nil
		})
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if workload.Status(&wl) == workload.StatusPending &&
		!features.Enabled(features.KueueDRAIntegration) &&
		features.Enabled(features.KueueDRARejectWorkloadsWhenDRADisabled) &&
		workload.HasDRA(&wl) {
		log.V(3).Info("Rejecting workload that uses DRA resources because KueueDRAIntegration feature gate is disabled")
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
			updated := workload.UnsetQuotaReservationWithCondition(wl, reason,
				"Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled",
				r.clock.Now())
			if workload.SetRequeuedCondition(wl, kueue.WorkloadInadmissible,
				"Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled", false) {
				updated = true
			}
			return updated, nil
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA rejection: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if workload.Status(&wl) == workload.StatusPending && dra.NeedsDRAReconcile(&wl, r.draBackedResources) {
		workload.AdjustResources(ctx, r.client, &wl)
		if workload.HasResourceClaim(&wl) {
			log.V(3).Info("Workload is inadmissible because it uses resource claims which is not supported")
			err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
				reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
				updated := workload.UnsetQuotaReservationWithCondition(wl, reason, "KueueDRAIntegration feature does not support use of resource claims", r.clock.Now())
				if updated && workload.SetRequeuedCondition(wl, kueue.WorkloadInadmissible, "DRA resource claims not supported", false) {
					updated = true
				}
				return updated, nil
			})
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA resource claims error: %w", err)
			}
			return ctrl.Result{}, nil
		}

		log.V(3).Info("Processing DRA resources for workload")

		// Process ResourceClaimTemplates (existing DRA path)
		draResources, fieldErrs := dra.GetResourceRequestsForResourceClaimTemplates(ctx, r.client, r.draMapper, &wl)
		if len(fieldErrs) > 0 {
			err := fieldErrs.ToAggregate()
			log.Error(err, "Failed to process DRA resources for workload")
			updateErr := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
				reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
				updated := workload.UnsetQuotaReservationWithCondition(wl, reason, err.Error(), r.clock.Now())
				if updated && workload.SetRequeuedCondition(wl, kueue.WorkloadInadmissible, err.Error(), false) {
					updated = true
				}
				return updated, nil
			})
			if updateErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA error: %w", updateErr)
			}
			if hasInternalError(fieldErrs) {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		// Process Extended Resources backed by DRA (new path)
		var extendedResources map[kueue.PodSetReference]corev1.ResourceList
		var replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName]
		if features.Enabled(features.KueueDRAIntegrationExtendedResource) {
			var extFieldErrs field.ErrorList
			extendedResources, replacedExtendedResources, extFieldErrs = dra.ResolveExtendedResourceQuota(ctx, r.client, r.draMapper, &wl)
			if len(extFieldErrs) > 0 {
				err := extFieldErrs.ToAggregate()
				log.Error(err, "Failed to process DRA extended resources for workload")
				updateErr := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
					reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
					updated := workload.UnsetQuotaReservationWithCondition(wl, reason, err.Error(), r.clock.Now())
					if updated && workload.SetRequeuedCondition(wl, kueue.WorkloadInadmissible, err.Error(), false) {
						updated = true
					}
					return updated, nil
				})
				if updateErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA extended resources error: %w", updateErr)
				}
				if hasInternalError(extFieldErrs) {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
		}

		// Merge extended resources into draResources.
		// When a DeviceClass appears in both paths, the extended resources path uses
		// the deviceClassMappings logical name as the quota key, unifying quota accounting.
		for podSetName, resources := range extendedResources {
			if existing, ok := draResources[podSetName]; ok {
				// Merge resources for the same podset
				draResources[podSetName] = resource.MergeResourceListKeepSum(existing, resources)
			} else {
				if draResources == nil {
					draResources = make(map[kueue.PodSetReference]corev1.ResourceList)
				}
				draResources[podSetName] = resources
			}
		}

		// Process counter-based resources for partitionable devices
		if features.Enabled(features.KueueDRAIntegrationPartitionableDevices) {
			counterResources, counterFieldErrs := dra.GetCounterResourcesForWorkload(ctx, r.client, r.draMapper, &wl)
			if len(counterFieldErrs) > 0 {
				err := counterFieldErrs.ToAggregate()
				log.Error(err, "Failed to process DRA counter resources for workload")
				updateErr := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
					reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
					updated := workload.UnsetQuotaReservationWithCondition(wl, reason, err.Error(), r.clock.Now())
					if updated && workload.SetRequeuedCondition(wl, kueue.WorkloadInadmissible, err.Error(), false) {
						updated = true
					}
					return updated, nil
				})
				if updateErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update workload status for DRA counter resources error: %w", updateErr)
				}
				if hasInternalError(counterFieldErrs) {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}
			for podSetName, resources := range counterResources {
				if existing, ok := draResources[podSetName]; ok {
					draResources[podSetName] = resource.MergeResourceListKeepSum(existing, resources)
				} else {
					if draResources == nil {
						draResources = make(map[kueue.PodSetReference]corev1.ResourceList)
					}
					draResources[podSetName] = resources
				}
			}
		}

		quotaReservedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
		requeuedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued)

		var conditionsCleared bool
		if quotaReservedCond != nil && quotaReservedCond.Status == metav1.ConditionFalse {
			apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			conditionsCleared = true
		}
		if requeuedCond != nil && requeuedCond.Status == metav1.ConditionFalse {
			apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadRequeued)
			conditionsCleared = true
		}

		if conditionsCleared {
			log.V(3).Info("Cleared previous inadmissible conditions after successful DRA processing")
		}

		var queueOptions []workload.InfoOption
		if len(draResources) > 0 || len(replacedExtendedResources) > 0 {
			queueOptions = append(queueOptions, workload.WithPreprocessedDRAResources(draResources, replacedExtendedResources))
		}

		if workload.IsAdmissible(&wl) {
			if err := r.queues.AddOrUpdateWorkload(log, wl.DeepCopy(), queueOptions...); err != nil {
				log.V(2).Info("Failed to add DRA workload to queue", "error", err)
				return ctrl.Result{}, err
			}
		} else {
			if !r.cache.AddOrUpdateWorkload(log, wl.DeepCopy()) {
				log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
			}
		}
		log.V(3).Info("Successfully pre-processed and queued DRA workload in scheduler")
	}

	if workload.IsActive(&wl) {
		if apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadDeactivationTarget) {
			wl.Spec.Active = new(false)
			err := r.client.Update(ctx, &wl)
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if wl.Status.RequeueState != nil && wl.Status.RequeueState.RequeueAt != nil {
			if requeueAfter := new(wl.Status.RequeueState.RequeueAt.Sub(r.clock.Now())); *requeueAfter > 0 {
				log.V(3).Info("Waiting for backoff to finish", "backoff", *requeueAfter)
				return reconcile.Result{RequeueAfter: *requeueAfter}, nil
			}

			if workload.Status(&wl) == workload.StatusPending {
				log.V(3).Info("Pending workload requeued after backoff")

				// Clear RequeueAt since backoff has elapsed
				err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
					if wl.Status.RequeueState != nil {
						wl.Status.RequeueState.RequeueAt = nil
					}
					return true, nil
				})
				if err != nil {
					return ctrl.Result{}, client.IgnoreNotFound(err)
				}

				if !workload.IsAdmissible(&wl) {
					log.V(3).Info("Workload is inadmissible after backoff, waiting for condition change", "workload", klog.KObj(&wl))
					return ctrl.Result{}, nil
				}

				if err := r.queues.AddOrUpdateWorkload(log, wl.DeepCopy()); err != nil {
					log.V(2).Info("failed to put the workload back into queue", "error", err)
					return ctrl.Result{}, err
				}

				log.V(3).Info("Workload requeued after backoff")
				return ctrl.Result{}, nil
			}
		}

		var updated bool
		var requeueAfter time.Duration
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			if cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued); cond != nil && cond.Status == metav1.ConditionFalse {
				switch cond.Reason {
				case kueue.WorkloadDeactivated:
					workload.SetRequeuedCondition(wl, kueue.WorkloadReactivated, "The workload was reactivated", true)
					updated = true
				case kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadEvictedByAdmissionCheck:

					if wl.Status.RequeueState != nil && wl.Status.RequeueState.RequeueAt != nil {
						requeueAfter = wl.Status.RequeueState.RequeueAt.Sub(r.clock.Now())
					}
					if requeueAfter > 0 {
						return updated, nil
					}
					if wl.Status.RequeueState != nil {
						wl.Status.RequeueState.RequeueAt = nil
					}
					workload.SetRequeuedCondition(wl, kueue.WorkloadBackoffFinished, "The workload backoff was finished", true)
					updated = true
				}
			}
			return updated, nil
		})

		if requeueAfter > 0 {
			return reconcile.Result{RequeueAfter: requeueAfter}, nil
		}
		if updated {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	} else {
		var updated, evicted bool
		var underlyingCause kueue.EvictionUnderlyingCause
		reason := kueue.WorkloadDeactivated
		message := "The workload is deactivated"
		dtCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadDeactivationTarget)
		if !workloadevict.IsEvicted(&wl) {
			if dtCond != nil {
				underlyingCause = kueue.EvictionUnderlyingCause(dtCond.Reason)
				message = fmt.Sprintf("%s due to %s", message, dtCond.Message)
			}
			updated = true
			evicted = true
		}
		if wl.Status.RequeueState != nil {
			updated = true
		}
		prepare := func(wl *kueue.Workload) {
			if dtCond != nil {
				apimeta.RemoveStatusCondition(&wl.Status.Conditions, kueue.WorkloadDeactivationTarget)
			}
			if wl.Status.RequeueState != nil {
				// Clear RequeueState using Merge Patch instead of SSA.
				// SSA cannot delete a pointer field with json:",omitempty" when it's owned
				// by a different field manager. Merge Patch handles this correctly.
				wl.Status.RequeueState = nil
			}
		}
		if updated {
			if evicted {
				exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, &wl)
				if err := workloadevict.Evict(
					ctx,
					r.client,
					r.recorder,
					&wl,
					reason,
					message,
					underlyingCause,
					r.clock,
					exposeLqMetrics,
					r.roleTracker,
					r.customLabels,
					workloadevict.WithCustomPrepare(prepare),
				); err != nil {
					if !apierrors.IsNotFound(err) {
						return ctrl.Result{}, fmt.Errorf("setting eviction: %w", err)
					}
				}
			} else {
				if err := clientutil.PatchStatus(ctx, r.client, &wl, func() (bool, error) {
					prepare(&wl)
					return true, nil
				}); err != nil {
					if !apierrors.IsNotFound(err) {
						return ctrl.Result{}, fmt.Errorf("updating workload: %w", err)
					}
				}
			}
			return ctrl.Result{}, nil
		}
	}

	lq := kueue.LocalQueue{}
	err := r.client.Get(ctx, types.NamespacedName{Namespace: wl.Namespace, Name: string(wl.Spec.QueueName)}, &lq)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	lqExists := err == nil
	lqActive := ptr.Deref(lq.Spec.StopPolicy, kueue.None) == kueue.None
	if lqExists && lqActive && isDisabledRequeuedByLocalQueueStopped(&wl) {
		return ctrl.Result{}, client.IgnoreNotFound(workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetRequeuedCondition(wl, kueue.WorkloadLocalQueueRestarted, "The LocalQueue was restarted after being stopped", true), nil
		}))
	}

	if features.Enabled(features.AdmissionGatedBy) {
		if conditionUpdated, err := r.mayUpdateConditionForAdmissionGatedBy(ctx, &wl); conditionUpdated || err != nil {
			return ctrl.Result{}, err
		}
	}

	cqName, cqOk := r.queues.ClusterQueueForWorkload(&wl)
	var cq *kueue.ClusterQueue
	if cqOk {
		// because we need to react to API cluster cq events, the list of checks from a cache can lead to race conditions
		cq = &kueue.ClusterQueue{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(cqName)}, cq); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		// If stopped cluster queue is started we need to set the WorkloadRequeued condition to true.
		if isDisabledRequeuedByClusterQueueStopped(&wl) && ptr.Deref(cq.Spec.StopPolicy, kueue.None) == kueue.None {
			return ctrl.Result{}, client.IgnoreNotFound(workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
				return workload.SetRequeuedCondition(wl, kueue.WorkloadClusterQueueRestarted, "The ClusterQueue was restarted after being stopped", true), nil
			}))
		}
		if updated, err := r.reconcileSyncAdmissionChecks(ctx, &wl, cq); updated || err != nil {
			return ctrl.Result{}, err
		}
	}

	// If the workload is admitted, updating the status here would set the Admitted condition to
	// false before the workloads eviction.
	if !workload.IsAdmitted(&wl) {
		var updated bool
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, &wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			if !workload.HasQuotaReservation(wl) {
				if changed, err := r.syncQuotaReservedFalseCondition(ctx, wl, lqExists, lqActive, cq); err != nil {
					return false, err
				} else if changed {
					updated = true
				}
			}
			if workload.SyncAdmittedCondition(wl, r.clock.Now()) {
				updated = true
			}
			return updated, nil
		})
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		isAdmitted := workload.IsAdmitted(&wl)
		if isAdmitted {
			queuedWaitTime := workload.QueuedWaitTime(&wl, r.clock)
			quotaReservedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			quotaReservedWaitTime := r.clock.Since(quotaReservedCondition.LastTransitionTime.Time)
			r.recorder.Eventf(
				&wl,
				nil,
				corev1.EventTypeNormal,
				"Admitted",
				"Admitted",
				"Admitted by ClusterQueue %v, wait time since reservation was %.0fs",
				wl.Status.Admission.ClusterQueue,
				quotaReservedWaitTime.Seconds(),
			)
			priorityClassName := workloadpatching.PriorityClassName(&wl)
			r.cache.ReportCohortSubtreeAdmittedWorkload(log, &wl)
			metrics.AdmittedWorkload(cqName, priorityClassName, queuedWaitTime, r.customLabels.CQGet(cqName), r.roleTracker)
			metrics.ReportAdmissionChecksWaitTime(cqName, priorityClassName, quotaReservedWaitTime, r.customLabels.CQGet(cqName), r.roleTracker)
			if r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, &wl) {
				lqRef := metrics.LQRefFromWorkload(&wl)
				lqKey := qutil.KeyFromWorkload(&wl)
				metrics.LocalQueueAdmittedWorkload(
					lqRef,
					priorityClassName,
					queuedWaitTime,
					r.customLabels.LQGet(lqKey),
					r.roleTracker,
				)
				metrics.ReportLocalQueueAdmissionChecksWaitTime(
					lqRef,
					priorityClassName,
					quotaReservedWaitTime,
					r.customLabels.LQGet(lqKey),
					r.roleTracker,
				)
			}
		}
	}

	if workload.HasQuotaReservation(&wl) {
		if evictionTriggered, err := r.reconcileCheckBasedEviction(ctx, &wl); evictionTriggered || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if updated, err := r.reconcileOnLocalQueueActiveState(ctx, &wl, lqExists, &lq); updated || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		if updated, err := r.reconcileOnClusterQueueActiveState(ctx, &wl, cqName); updated || err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		podsReadyRecheckAfter, err := r.reconcileNotReadyTimeout(ctx, req, &wl)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		maxExecRecheckAfter, err := r.reconcileMaxExecutionTime(ctx, &wl)
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

		// get the minimun non-zero value
		recheckAfter := min(podsReadyRecheckAfter, maxExecRecheckAfter)
		if recheckAfter == 0 {
			recheckAfter = max(podsReadyRecheckAfter, maxExecRecheckAfter)
		}
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	}

	return ctrl.Result{}, nil
}

// isOrphanedWorkload determines if a workload is orphaned and should be finalized.
//
// A workload is considered orphaned when it meets **both** of the following conditions:
//  1. It has no OwnerReferences.
//  2. Either:
//     - Its DeletionTimestamp is set (it's in the process of being deleted), OR
//     - It has a JobUIDLabel (controllerconsts.JobUIDLabel), meaning it was
//     previously owned by a Job whose OwnerReference was later removed.
func isOrphanedWorkload(wl *kueue.Workload) bool {
	// If it has an owner, it cannot be orphaned.
	if len(wl.OwnerReferences) > 0 {
		return false
	}

	// Check if the workload is actively being deleted.
	if !wl.DeletionTimestamp.IsZero() {
		return true
	}

	// Check if the workload is associated with a specific Job UID.
	return features.Enabled(features.FinishOrphanedWorkloads) && wl.Labels[controllerconsts.JobUIDLabel] != ""
}

// isDisabledRequeuedByClusterQueueStopped returns true if the workload is unset requeued by cluster queue stopped.
func isDisabledRequeuedByClusterQueueStopped(w *kueue.Workload) bool {
	return isDisabledRequeuedByReason(w, kueue.WorkloadEvictedByClusterQueueStopped)
}

// isDisabledRequeuedByLocalQueueStopped returns true if the workload is unset requeued by local queue stopped.
func isDisabledRequeuedByLocalQueueStopped(w *kueue.Workload) bool {
	return isDisabledRequeuedByReason(w, kueue.WorkloadEvictedByLocalQueueStopped)
}

// isDisabledRequeuedByReason returns true if the workload is unset requeued by reason.
func isDisabledRequeuedByReason(w *kueue.Workload, reason string) bool {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadRequeued)
	return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == reason
}

// reconcileMaxExecutionTime deactivates the workload if its MaximumExecutionTimeSeconds is exceeded or returns a retry after value.
func (r *WorkloadReconciler) reconcileMaxExecutionTime(ctx context.Context, wl *kueue.Workload) (time.Duration, error) {
	admittedCondition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCondition == nil || admittedCondition.Status != metav1.ConditionTrue || wl.Spec.MaximumExecutionTimeSeconds == nil {
		return 0, nil
	}

	remainingTime := time.Duration(
		*wl.Spec.MaximumExecutionTimeSeconds-ptr.Deref(wl.Status.AccumulatedPastExecutionTimeSeconds, 0),
	)*time.Second - r.clock.Since(
		admittedCondition.LastTransitionTime.Time,
	)
	if remainingTime > 0 {
		return remainingTime, nil
	}

	if !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadDeactivationTarget) {
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			updated := workload.SetDeactivationTarget(wl, kueue.WorkloadMaximumExecutionTimeExceeded, "exceeding the maximum execution time")
			if wl.Status.AccumulatedPastExecutionTimeSeconds != nil {
				wl.Status.AccumulatedPastExecutionTimeSeconds = nil
				updated = true
			}
			return updated, nil
		})
		if err != nil {
			return 0, err
		}
		r.recorder.Eventf(
			wl,
			nil,
			corev1.EventTypeWarning,
			kueue.WorkloadMaximumExecutionTimeExceeded,
			"MaximumExecutionTimeExceeded",
			"The maximum execution time (%ds) exceeded",
			*wl.Spec.MaximumExecutionTimeSeconds,
		)
	}
	return 0, nil
}

// buildAdmissionChecksMessage formats a human-readable message
// describing the list of admission checks in the given state.
func buildAdmissionChecksMessage(checks []kueue.AdmissionCheckState, state kueue.CheckState) string {
	slices.SortFunc(checks, func(a, b kueue.AdmissionCheckState) int {
		return cmp.Compare(a.Name, b.Name)
	})
	parts := make([]string, 0, len(checks))
	for _, ac := range checks {
		if ac.Message != "" {
			parts = append(parts, fmt.Sprintf("%q (%s)", ac.Name, ac.Message))
		} else {
			parts = append(parts, fmt.Sprintf("%q", ac.Name))
		}
	}
	noun := "AdmissionCheck"
	if len(checks) > 1 {
		noun = "AdmissionChecks"
	}
	return fmt.Sprintf("%s in %s state: %s", noun, state, stringsutils.Join(parts, "; "))
}

// reconcileCheckBasedEviction evicts or deactivates the given Workload if any admission checks have failed.
// Returns true if the Workload was rejected or deactivated, and false otherwise.
func (r *WorkloadReconciler) reconcileCheckBasedEviction(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsParent(wl) {
		// Parent Workloads are not supposed to have admission checks.
		return false, nil
	}
	if workloadevict.IsEvicted(wl) || (!workload.HasRetryChecks(wl) && !workload.HasRejectedChecks(wl)) {
		return false, nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Workload is evicted due to admission checks")
	if workload.HasRejectedChecks(wl) {
		rejectedChecks := workload.RejectedChecks(wl)
		message := buildAdmissionChecksMessage(rejectedChecks, kueue.CheckStateRejected)
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetDeactivationTarget(wl, kueue.WorkloadEvictedByAdmissionCheck, message), nil
		})
		if err != nil {
			return false, err
		}
		log.V(3).Info("Workload is deactivated due to rejected admission checks", "workload", klog.KObj(wl), "rejectedChecks", rejectedChecks)
		r.recorder.Eventf(wl, nil, corev1.EventTypeWarning, "AdmissionCheckRejected", "AdmissionCheckRejected", api.TruncateEventMessage(fmt.Sprintf("Deactivated due to %s", message)))
		return true, nil
	}
	// at this point we know a Workload has at least one Retry AdmissionCheck
	retryChecks := workload.RetryChecks(wl)
	message := fmt.Sprintf("Evicted due to %s", buildAdmissionChecksMessage(retryChecks, kueue.CheckStateRetry))
	exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl)
	if err := workloadevict.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByAdmissionCheck, message, "", r.clock, exposeLqMetrics, r.roleTracker, r.customLabels); err != nil {
		return false, err
	}
	return true, nil
}

func (r *WorkloadReconciler) reconcileSyncAdmissionChecks(ctx context.Context, wl *kueue.Workload, cq *kueue.ClusterQueue) (bool, error) {
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsParent(wl) {
		// Parent Workloads are not supposed to have admission checks.
		return false, nil
	}
	log := ctrl.LoggerFrom(ctx)
	admissionChecks := workload.AdmissionChecksForWorkload(log, wl, cq)
	newChecks, shouldUpdate := syncAdmissionCheckConditions(wl.Status.AdmissionChecks, admissionChecks, r.clock)
	if shouldUpdate {
		log.V(3).Info("The workload needs admission checks updates", "clusterQueue", klog.KRef("", cq.Name), "admissionChecks", admissionChecks)
		wl.Status.AdmissionChecks = newChecks
		err := r.client.Status().Update(ctx, wl)
		return true, client.IgnoreNotFound(err)
	}
	return false, nil
}

func (r *WorkloadReconciler) syncPreemptionGateStates(wl *kueue.Workload) bool {
	changed := false

	preemptionGates := make(map[string]kueue.PreemptionGate)
	for _, gate := range wl.Spec.PreemptionGates {
		preemptionGates[gate.Name] = gate
	}
	wl.Status.PreemptionGates = slices.DeleteFunc(wl.Status.PreemptionGates, func(gateState kueue.PreemptionGateState) bool {
		_, ok := preemptionGates[gateState.Name]
		if !ok {
			changed = true
		}

		return !ok
	})

	preemptionGateStates := make(map[string]kueue.PreemptionGateState)
	for _, gateState := range wl.Status.PreemptionGates {
		preemptionGateStates[gateState.Name] = gateState
	}
	for _, gate := range wl.Spec.PreemptionGates {
		if _, ok := preemptionGateStates[gate.Name]; !ok {
			wl.Status.PreemptionGates = append(wl.Status.PreemptionGates, kueue.PreemptionGateState{
				Name:               gate.Name,
				Position:           kueue.PreemptionGatePositionClosed,
				LastTransitionTime: metav1.NewTime(r.clock.Now()),
			})
			changed = true
		}
	}

	return changed
}

func (r *WorkloadReconciler) reconcileOnLocalQueueActiveState(ctx context.Context, wl *kueue.Workload, lqExists bool, lq *kueue.LocalQueue) (bool, error) {
	queueStopPolicy := ptr.Deref(lq.Spec.StopPolicy, kueue.None)

	log := ctrl.LoggerFrom(ctx)

	if workload.IsAdmitted(wl) {
		if queueStopPolicy != kueue.HoldAndDrain {
			return false, nil
		}
		if workloadevict.IsEvicted(wl) {
			log.V(3).Info("Workload is already evicted.")
			return false, nil
		}
		log.V(3).Info("Workload is evicted because the LocalQueue is stopped", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
		exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl)
		err := workloadevict.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByLocalQueueStopped, "The LocalQueue is stopped", "", r.clock, exposeLqMetrics, r.roleTracker, r.customLabels)
		return true, err
	}

	if !lqExists || !lq.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is inadmissible because the LocalQueue is terminating or missing", "localQueue", klog.KRef("", string(wl.Spec.QueueName)))
		return true, workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
			return workload.UnsetQuotaReservationWithCondition(wl, reason, fmt.Sprintf("LocalQueue %s is terminating or missing", wl.Spec.QueueName), r.clock.Now()), nil
		})
	}

	if queueStopPolicy != kueue.None {
		log.V(3).Info("Workload is inadmissible because the LocalQueue is stopped", "localQueue", klog.KRef("", string(wl.Spec.QueueName)))
		return true, workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonSuspended, kueue.WorkloadInadmissible)
			return workload.UnsetQuotaReservationWithCondition(wl, reason, fmt.Sprintf("LocalQueue %s is stopped", wl.Spec.QueueName), r.clock.Now()), nil
		})
	}

	return false, nil
}

func (r *WorkloadReconciler) reconcileOnClusterQueueActiveState(ctx context.Context, wl *kueue.Workload, cqName kueue.ClusterQueueReference) (bool, error) {
	cq := kueue.ClusterQueue{}
	err := r.client.Get(ctx, types.NamespacedName{Name: string(cqName)}, &cq)
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	cqExists := err == nil

	queueStopPolicy := ptr.Deref(cq.Spec.StopPolicy, kueue.None)

	log := ctrl.LoggerFrom(ctx)
	if workload.IsAdmitted(wl) {
		if queueStopPolicy != kueue.HoldAndDrain {
			return false, nil
		}
		if workloadevict.IsEvicted(wl) {
			log.V(3).Info("Workload is already evicted.")
			return false, nil
		}
		log.V(3).Info("Workload is evicted because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", string(cqName)))
		message := "The ClusterQueue is stopped"
		exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl)
		err := workloadevict.Evict(ctx, r.client, r.recorder, wl, kueue.WorkloadEvictedByClusterQueueStopped, message, "", r.clock, exposeLqMetrics, r.roleTracker, r.customLabels)
		return true, err
	}

	if !cqExists || !cq.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is terminating or missing", "clusterQueue", klog.KRef("", string(cqName)))
		return true, workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonMisconfigured, kueue.WorkloadInadmissible)
			return workload.UnsetQuotaReservationWithCondition(wl, reason, fmt.Sprintf("ClusterQueue %s is terminating or missing", cqName), r.clock.Now()), nil
		})
	}

	if queueStopPolicy != kueue.None {
		log.V(3).Info("Workload is inadmissible because the ClusterQueue is stopped", "clusterQueue", klog.KRef("", string(cqName)))
		return true, workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonSuspended, kueue.WorkloadInadmissible)
			return workload.UnsetQuotaReservationWithCondition(wl, reason, fmt.Sprintf("ClusterQueue %s is stopped", cqName), r.clock.Now()), nil
		})
	}

	return false, nil
}

// mayUpdateConditionForAdmissionGatedBy updates the Condition of a Workload when it first detects that it is
// gated by an AdmissionGate or it detects that its AdmissionGate just got removed.
// Returns whether the function updated the condition or not.
// Returns error if the condition update fails.
// The function also emits Events after it successfully updates the Condition
func (r *WorkloadReconciler) mayUpdateConditionForAdmissionGatedBy(ctx context.Context, wl *kueue.Workload) (bool, error) {
	if !features.Enabled(features.AdmissionGatedBy) {
		return false, nil
	}

	hasGatedAnnotation := workload.HasAdmissionGate(wl)
	hasGatedCondition := hasAdmissionGatedCondition(wl)

	if !hasGatedAnnotation && hasGatedCondition {
		// This previously gated workload is becoming admissible because its AdmissionGatedBy annotation is cleared
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			reason := workload.UnadmittedWorkloadReasonWithFallback(
				kueue.WorkloadQuotaReservedReasonPendingEvaluation,
				kueue.WorkloadPending, //nolint:staticcheck // SA1019: fallback
			)
			// Update the condition to indicate the gate is cleared, rather than removing it
			return workload.UnsetQuotaReservationWithCondition(wl, reason, "AdmissionGatedBy cleared, waiting for quota reservation", r.clock.Now()), nil
		})
		if err != nil {
			return false, err
		}
		r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "AdmissionGateCleared", "AdmissionGateCleared",
			"Admission gate cleared, workload is now admissible")
		return true, nil
	} else if hasGatedAnnotation && !hasGatedCondition {
		// This is the first detection we see a non-empty AdmissionGatedBy annotation
		err := workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workload.UnsetQuotaReservationWithCondition(
				wl,
				kueue.WorkloadAdmissionGated,
				fmt.Sprintf("Admission is gated by: %s", wl.Annotations[constants.AdmissionGatedByAnnotation]),
				r.clock.Now(),
			), nil
		})
		if err != nil {
			return false, err
		}
		r.recorder.Eventf(wl, nil, corev1.EventTypeNormal, "AdmissionGated", "AdmissionGated",
			"Workload admission is gated by: %s", wl.Annotations[constants.AdmissionGatedByAnnotation])

		return true, nil
	}

	return false, nil
}

// hasAdmissionGatedCondition returns true if the workload has a QuotaReserved
// condition with reason AdmissionGated.
func hasAdmissionGatedCondition(wl *kueue.Workload) bool {
	condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	return condition != nil && condition.Reason == kueue.WorkloadAdmissionGated
}

func syncAdmissionCheckConditions(conds []kueue.AdmissionCheckState, admissionChecks sets.Set[kueue.AdmissionCheckReference], c clock.Clock) ([]kueue.AdmissionCheckState, bool) {
	if len(admissionChecks) == 0 {
		return nil, len(conds) > 0
	}

	shouldUpdate := false
	currentChecks := utilslices.ToRefMap(conds, func(c *kueue.AdmissionCheckState) kueue.AdmissionCheckReference { return c.Name })
	for t := range admissionChecks {
		if _, found := currentChecks[t]; !found {
			workloadpatching.SetAdmissionCheckState(&conds, kueue.AdmissionCheckState{
				Name:  t,
				State: kueue.CheckStatePending,
			}, c)
			shouldUpdate = true
		}
	}

	// if the workload conditions length is bigger, then some cleanup should be done
	if len(conds) > len(admissionChecks) {
		newConds := make([]kueue.AdmissionCheckState, 0, len(admissionChecks))
		shouldUpdate = true
		for i := range conds {
			c := &conds[i]
			if admissionChecks.Has(c.Name) {
				newConds = append(newConds, *c)
			}
		}
		conds = newConds
	}
	slices.SortFunc(conds, func(state1, state2 kueue.AdmissionCheckState) int {
		return cmp.Compare(state1.Name, state2.Name)
	})
	return conds, shouldUpdate
}

func (r *WorkloadReconciler) reconcileNotReadyTimeout(ctx context.Context, req ctrl.Request, wl *kueue.Workload) (time.Duration, error) {
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsVariant(wl) {
		// Variant Workloads are not supposed to have PodsReady condition, it's Parent Workload responsibility.
		return 0, nil
	}
	log := ctrl.LoggerFrom(ctx)

	if !workload.IsActive(wl) || workloadevict.IsEvicted(wl) {
		// the workload has already been evicted by the PodsReadyTimeout or been deactivated.
		return 0, nil
	}

	underlyingCause, recheckAfter := r.admittedNotReadyWorkload(wl)
	if underlyingCause == "" {
		return 0, nil
	}
	if recheckAfter > 0 {
		log.V(4).Info("Workload not yet ready and did not exceed its timeout", "recheckAfter", recheckAfter)
		return recheckAfter, nil
	}

	deactivated, err := r.triggerDeactivation(ctx, wl)
	if err != nil || deactivated {
		return 0, err
	}

	// Increments a re-queueing count and update a time to be re-queued.
	log.V(2).Info("Start the eviction of the workload due to exceeding the PodsReady timeout")
	message := fmt.Sprintf("Exceeded the PodsReady timeout %s", req.String())
	exposeLqMetrics := r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl)
	err = workloadevict.Evict(
		ctx,
		r.client,
		r.recorder,
		wl,
		kueue.WorkloadEvictedByPodsReadyTimeout,
		message,
		underlyingCause,
		r.clock,
		exposeLqMetrics,
		r.roleTracker,
		r.customLabels,
		workloadevict.WithCustomPrepare(func(wl *kueue.Workload) {
			workload.UpdateRequeueState(wl, r.waitForPodsReady.requeuingBackoffBaseSeconds, int32(r.waitForPodsReady.requeuingBackoffMaxDuration.Seconds()), r.clock)
		}),
	)

	return 0, err
}

// triggerDeactivation trigger deactivation of workload
// if a re-queued number has already exceeded the limit of re-queuing backoff.
// It returns true as a first value if a workload triggered deactivation.
func (r *WorkloadReconciler) triggerDeactivation(ctx context.Context, wl *kueue.Workload) (bool, error) {
	requeueState := ptr.Deref(wl.Status.RequeueState, kueue.RequeueState{})
	// If requeuingBackoffLimitCount equals to null, the workloads is repeatedly and endless re-queued.
	if r.waitForPodsReady.requeuingBackoffLimitCount != nil && ptr.Deref(requeueState.Count, 0)+1 > *r.waitForPodsReady.requeuingBackoffLimitCount {
		if err := workloadpatching.PatchAdmissionStatus(ctx, r.client, wl, r.clock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetDeactivationTarget(wl, kueue.WorkloadRequeuingLimitExceeded, "exceeding the maximum number of re-queuing retries"), nil
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (r *WorkloadReconciler) Create(e event.TypedCreateEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(nil, e.Object)
	status := workload.Status(e.Object)
	log := r.logger().WithValues("workload", klog.KObj(e.Object), "queue", e.Object.Spec.QueueName, "status", status)
	log.V(2).Info("Workload create event")

	if status == workload.StatusFinished {
		r.queues.AddFinishedWorkload(e.Object)
		return true
	}

	ctx := ctrl.LoggerInto(context.Background(), log)
	wlCopy := e.Object.DeepCopy()
	workload.AdjustResources(ctx, r.client, wlCopy)

	if dra.NeedsDRAReconcile(e.Object, r.draBackedResources) {
		log.V(2).Info("Skipping DRA workload in Create event - will be handled in Reconcile")
		return true
	}

	if workload.IsAdmissible(e.Object) {
		if err := r.queues.AddOrUpdateWorkload(log, wlCopy); err != nil {
			log.V(2).Info("ignored an error for now", "error", err)
		}
		return true
	}
	if !r.cache.AddOrUpdateWorkload(log, wlCopy) {
		log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
	}
	r.queues.QueueSecondPassIfNeeded(ctx, wlCopy, 0)
	return true
}

func (r *WorkloadReconciler) Delete(e event.TypedDeleteEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(e.Object, nil)
	status := "unknown"
	if !e.DeleteStateUnknown {
		status = workload.Status(e.Object)
	}
	log := r.logger().WithValues("workload", klog.KObj(e.Object), "queue", e.Object.Spec.QueueName, "status", status)
	log.V(2).Info("Workload delete event")
	r.preemptionExpectations.ObservedUID(log,
		client.ObjectKeyFromObject(e.Object), e.Object.UID)

	ctx := ctrl.LoggerInto(context.Background(), log)
	wlKey := workload.Key(e.Object)

	// Delete from cache unconditionally. Pending workloads may have been "assumed"
	// by the scheduler, and leaving them blocks ClusterQueue finalizer removal.
	// The operation is idempotent if the workload was never in the cache.
	r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wlKey, func() {
		if err := r.cache.DeleteWorkload(log, wlKey); err != nil {
			log.Error(err, "Failed to delete workload from cache")
		}
	})

	// Even if the state is unknown, the last cached state tells us whether the
	// workload was in the queues and should be cleared from them.
	r.queues.DeleteAndForgetWorkload(log, wlKey)
	return true
}

func (r *WorkloadReconciler) Update(e event.TypedUpdateEvent[*kueue.Workload]) bool {
	defer r.notifyWatchers(e.ObjectOld, e.ObjectNew)

	status := workload.Status(e.ObjectNew)
	log := r.logger().WithValues("workload", klog.KObj(e.ObjectNew), "queue", e.ObjectNew.Spec.QueueName, "status", status)
	ctx := ctrl.LoggerInto(context.Background(), log)
	active := workload.IsActive(e.ObjectNew)

	prevQueue := e.ObjectOld.Spec.QueueName
	if prevQueue != e.ObjectNew.Spec.QueueName {
		log = log.WithValues("prevQueue", prevQueue)
	}
	prevStatus := workload.Status(e.ObjectOld)
	if prevStatus != status {
		log = log.WithValues("prevStatus", prevStatus)
	}
	if workload.HasQuotaReservation(e.ObjectNew) {
		log = log.WithValues("clusterQueue", e.ObjectNew.Status.Admission.ClusterQueue)
	}
	if workload.HasQuotaReservation(e.ObjectOld) && (!workload.HasQuotaReservation(e.ObjectNew) || e.ObjectNew.Status.Admission.ClusterQueue != e.ObjectOld.Status.Admission.ClusterQueue) {
		log = log.WithValues("prevClusterQueue", e.ObjectOld.Status.Admission.ClusterQueue)
	}
	log.V(2).Info("Workload update event")

	wlCopy := e.ObjectNew.DeepCopy()
	wlKey := workload.Key(e.ObjectNew)
	// We do not handle old workload here as it will be deleted or replaced by new one anyway.
	workload.AdjustResources(ctrl.LoggerInto(ctx, log), r.client, wlCopy)

	onHold := workload.IsOnHold(wlCopy)

	switch {
	case status == workload.StatusFinished || !active:
		if !active {
			log.V(2).Info("Workload will not be queued because the workload is not active")
		}

		if status == workload.StatusFinished && prevStatus != workload.StatusFinished {
			r.reportFinishedWorkload(log, wlCopy)
		}

		// The workload could have been in the queues if we missed an event.
		r.queues.DeleteWorkload(log, wlKey)
		r.queues.AddFinishedWorkload(wlCopy)

		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wlKey, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(log, wlKey); err != nil && prevStatus == workload.StatusAdmitted {
				log.Error(err, "Failed to delete workload from cache")
			}
		})
	case prevStatus == workload.StatusPending && status == workload.StatusPending:
		switch {
		case onHold:
			log.V(2).Info("Skipping queue update for workload on hold")
		case dra.NeedsDRAReconcile(e.ObjectNew, r.draBackedResources):
			log.V(2).Info("Skipping queue update for DRA workload - handled in Reconcile")
		default:
			if err := r.queues.AddOrUpdateWorkload(log, wlCopy); err != nil {
				log.V(2).Info("ignored an error for now", "error", err)
			}
		}
	case prevStatus == workload.StatusPending && (status == workload.StatusQuotaReserved || status == workload.StatusAdmitted):
		r.queues.DeleteWorkload(log, wlKey)
		if !r.cache.AddOrUpdateWorkload(log, wlCopy) {
			log.V(2).Info("ClusterQueue for workload didn't exist; ignored for now")
		}
		if afs.Enabled(r.admissionFSConfig) && status == workload.StatusAdmitted && r.cache.ClusterQueueUsesAdmissionFairSharing(wlCopy.Status.Admission.ClusterQueue) {
			r.updateAfsConsumedUsage(log, wlCopy)
		}
	case workload.FromQuotaReservedOrAdmittedToPending(prevStatus, status):
		if cq, reason, latency, ok := workload.EvictionPendingLatency(e.ObjectOld, e.ObjectNew, r.clock.Now()); ok {
			metrics.ReportWorkloadEvictionLatency(cq, reason, latency, r.customLabels.CQGet(cq), r.roleTracker)
		}
		var backoff time.Duration
		if wlCopy.Status.RequeueState != nil && wlCopy.Status.RequeueState.RequeueAt != nil {
			backoff = time.Until(e.ObjectNew.Status.RequeueState.RequeueAt.Time)
		}
		immediate := backoff <= 0
		log.V(3).Info("Workload transitioned back to pending", "backoff", backoff, "immediate", immediate)
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wlKey, func() {
			// Delete the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			if err := r.cache.DeleteWorkload(log, wlKey); err != nil {
				log.Error(err, "Failed to delete workload from cache")
			}
			// Here we don't take the lock as it is already taken by the wrapping function.
			// The delayed requeue is done in the Reconcile function, but we need to
			// keep the immediate retry here because it will ensure that
			// AddOrUpdateWorkload is only called once. When moving it to the main
			// reconciler, we would execute it on every run, which might mess up the state.
			if immediate {
				switch {
				case onHold:
					log.V(2).Info("Skipping immediate requeue for on-hold workload")
				case dra.NeedsDRAReconcile(e.ObjectNew, r.draBackedResources):
					log.V(2).Info("Skipping immediate requeue for DRA workload - handled in Reconcile")
				default:
					if err := r.queues.AddOrUpdateWorkloadWithoutLock(log, wlCopy); err != nil {
						log.V(2).Info("ignored an error for now", "error", err)
					}
					r.queues.DeleteSecondPassWithoutLock(wlKey)
				}
			}

			// Clean up second pass when the updated workload no longer needs it.
			if !workload.NeedsSecondPass(wlCopy) {
				r.queues.DeleteSecondPassWithoutLock(wlKey)
			}
		})
	case prevStatus == workload.StatusAdmitted && status == workload.StatusAdmitted && !equality.Semantic.DeepEqual(e.ObjectOld.Status.ReclaimablePods, e.ObjectNew.Status.ReclaimablePods),
		features.Enabled(features.ElasticJobsViaWorkloadSlices) && workloadslicing.ScaledDown(workload.ExtractPodSetCountsFromWorkload(e.ObjectOld), workload.ExtractPodSetCountsFromWorkload(e.ObjectNew)),
		workload.PriorityChanged(log, e.ObjectOld, e.ObjectNew):
		// trigger the move of associated inadmissibleWorkloads, if there are any.
		r.queues.QueueAssociatedInadmissibleWorkloadsAfter(ctx, wlKey, func() {
			// Update the workload from cache while holding the queues lock
			// to guarantee that requeued workloads are taken into account before
			// the next scheduling cycle.
			r.cache.AddOrUpdateWorkload(log, wlCopy)
		})

	default:
		// Workload update in the cache is handled here; however, some fields are immutable
		// and are not supposed to actually change anything.
		r.cache.AddOrUpdateWorkload(log, wlCopy)
	}
	r.queues.QueueSecondPassIfNeeded(ctx, wlCopy, 0)
	return true
}

func (r *WorkloadReconciler) Generic(e event.TypedGenericEvent[*kueue.Workload]) bool {
	r.logger().V(3).Info("Ignore Workload generic event", "workload", klog.KObj(e.Object))
	return false
}

func (r *WorkloadReconciler) updateAfsConsumedUsage(log logr.Logger, wl *kueue.Workload) {
	lqKey := qutil.KeyFromWorkload(wl)
	penalty := afs.CalculateEntryPenalty(workload.NewInfo(wl).SumTotalRequests(), r.admissionFSConfig)
	now := r.clock.Now()

	oldEntry, found := r.queues.AfsConsumedResources.Get(lqKey)
	if !found {
		oldEntry.LastUpdate = now
	}

	cacheLq, err := r.cache.GetCacheLocalQueue(wl.Status.Admission.ClusterQueue, lqKey)
	if err != nil {
		log.V(2).Info("Failed to get cache LocalQueue", "error", err)
		return
	}

	oldUsage := oldEntry.Resources
	newUsage := cacheLq.GetAdmittedUsage()
	elapsed := now.Sub(oldEntry.LastUpdate).Seconds()
	newConsumed := afs.CalculateDecayedConsumed(oldUsage, newUsage, elapsed, r.admissionFSConfig.UsageHalfLifeTime.Seconds())
	newConsumed = resource.MergeResourceListKeepSum(newConsumed, penalty)

	r.queues.AfsConsumedResources.Set(lqKey, newConsumed, now)
	r.queues.AfsEntryPenalties.Sub(lqKey, penalty)
	log.V(3).Info("Entry penalty subtracted from localQueue", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)), "penalty", penalty, "remaining", r.queues.AfsEntryPenalties.Peek(lqKey))

	log.V(2).Info("Updated AFS consumed usage", "localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)), "consumed", newConsumed)
}

func (r *WorkloadReconciler) notifyWatchers(oldWl, newWl *kueue.Workload) {
	for _, w := range r.watchers {
		w.NotifyWorkloadUpdate(oldWl, newWl)
	}
}

func (r *WorkloadReconciler) reportFinishedWorkload(log logr.Logger, wl *kueue.Workload) {
	priorityClassName := workloadpatching.PriorityClassName(wl)
	cqName := ptr.Deref(wl.Status.Admission, kueue.Admission{}).ClusterQueue
	metrics.IncrementFinishedWorkloadTotal(cqName, priorityClassName, r.customLabels.CQGet(cqName), r.roleTracker)
	lqRef := metrics.LQRefFromWorkload(wl)
	if r.cache.ShouldExposeLocalQueueMetricsForWorkload(log, wl) {
		metrics.IncrementLocalQueueFinishedWorkloadTotal(lqRef, priorityClassName, r.customLabels.LQGet(qutil.KeyFromWorkload(wl)), r.roleTracker)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	ruh := &resourceUpdatesHandler{r: r}
	wqh := &workloadQueueHandler{r: r}
	deh := &draEventHandler{}
	dch := &deviceClassHandler{r: r}
	bld := builder.TypedControllerManagedBy[reconcile.Request](mgr).
		Named("workload_controller").
		WatchesRawSource(source.TypedKind(
			mgr.GetCache(),
			&kueue.Workload{},
			&handler.TypedEnqueueRequestForObject[*kueue.Workload]{},
			r,
		)).
		WatchesRawSource(source.Channel(r.draReconcileChannel, deh)).
		WithOptions(controller.Options{
			NeedLeaderElection:      new(false),
			MaxConcurrentReconciles: mgr.GetControllerOptions().GroupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String()],
			LogConstructor:          roletracker.NewLogConstructor(r.roleTracker, "workload-reconciler"),
		}).
		Watches(&corev1.LimitRange{}, ruh).
		Watches(&nodev1.RuntimeClass{}, ruh).
		Watches(&kueue.ClusterQueue{}, wqh).
		Watches(&kueue.LocalQueue{}, wqh)
	if features.Enabled(features.KueueDRAIntegrationExtendedResource) {
		if _, err := mgr.GetRESTMapper().RESTMapping(resourcev1.SchemeGroupVersion.WithKind("DeviceClass").GroupKind()); err != nil && apimeta.IsNoMatchError(err) {
			r.logger().V(2).Info("DeviceClass API not available, skipping DeviceClass watcher")
		} else if err != nil {
			return err
		} else {
			bld = bld.Watches(&resourcev1.DeviceClass{}, dch)
		}
	}
	return bld.Complete(WithLeadingManager(mgr, r, &kueue.Workload{}, cfg))
}

// admittedNotReadyWorkload returns the underlying cause and remaining time for
// a workload that is admitted but not yet in PodsReady condition.
//
// If the workload is not admitted, PodsReady is true, or no timeout is configured,
// it returns an empty underlyingCause and zero duration.
func (r *WorkloadReconciler) admittedNotReadyWorkload(wl *kueue.Workload) (kueue.EvictionUnderlyingCause, time.Duration) {
	if r.waitForPodsReady == nil {
		// the timeout is not configured for the workload controller
		return "", 0
	}
	if !workload.IsAdmitted(wl) {
		// the workload is not admitted so there is no need to time it out
		return "", 0
	}

	podsReadyCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
	if podsReadyCond != nil && podsReadyCond.Status == metav1.ConditionTrue {
		return "", 0
	}

	if podsReadyCond == nil || podsReadyCond.Reason == kueue.WorkloadWaitForStart || podsReadyCond.Reason == "PodsReady" {
		admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		elapsedTime := r.clock.Since(admittedCond.LastTransitionTime.Time)
		return kueue.WorkloadWaitForStart, max(r.waitForPodsReady.timeout-elapsedTime, 0)
	} else if podsReadyCond.Reason == kueue.WorkloadWaitForRecovery && r.waitForPodsReady.recoveryTimeout != nil {
		// A pod has failed and the workload is waiting for recovery
		elapsedTime := r.clock.Since(podsReadyCond.LastTransitionTime.Time)
		return kueue.WorkloadWaitForRecovery, max(*r.waitForPodsReady.recoveryTimeout-elapsedTime, 0)
	}
	return "", 0
}

type resourceUpdatesHandler struct {
	r *WorkloadReconciler
}

func (h *resourceUpdatesHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Create event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.ObjectNew.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Update event")
	h.handle(ctx, e.ObjectNew, q)
}

func (h *resourceUpdatesHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("kind", e.Object.GetObjectKind())
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Delete event")
	h.handle(ctx, e.Object, q)
}

func (h *resourceUpdatesHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *resourceUpdatesHandler) handle(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	switch v := obj.(type) {
	case *corev1.LimitRange:
		log := ctrl.LoggerFrom(ctx).WithValues("limitRange", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.InNamespace(v.Namespace))
	case *nodev1.RuntimeClass:
		log := ctrl.LoggerFrom(ctx).WithValues("runtimeClass", klog.KObj(v))
		ctx = ctrl.LoggerInto(ctx, log)
		h.queueReconcileForPending(ctx, q, client.MatchingFields{indexer.WorkloadRuntimeClassKey: v.Name})
	default:
		panic(v)
	}
}

func (h *resourceUpdatesHandler) queueReconcileForPending(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], opts ...client.ListOption) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	opts = append(opts, client.MatchingFields{indexer.WorkloadQuotaReservedKey: string(metav1.ConditionFalse)})
	err := h.r.client.List(ctx, &lst, opts...)
	if err != nil {
		log.Error(err, "Could not list pending workloads")
	}
	log.V(4).Info("Updating pending workload requests", "count", len(lst.Items))
	for _, w := range lst.Items {
		wlCopy := w.DeepCopy()
		log := log.WithValues("workload", klog.KObj(wlCopy))
		log.V(5).Info("Queue reconcile for")
		workload.AdjustResources(ctrl.LoggerInto(ctx, log), h.r.client, wlCopy)

		if dra.NeedsDRAReconcile(wlCopy, h.r.draBackedResources) {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      wlCopy.Name,
					Namespace: wlCopy.Namespace,
				},
			}
			q.Add(req)
			log.V(2).Info("Queued reconcile for DRA workload due to resource update")
			continue
		}

		if workload.IsAdmissible(wlCopy) {
			if err = h.r.queues.AddOrUpdateWorkload(log, wlCopy); err != nil {
				log.V(2).Info("ignored an error for now", "error", err)
			}
		}
	}
}

type workloadQueueHandler struct {
	r *WorkloadReconciler
}

var _ handler.EventHandler = (*workloadQueueHandler)(nil)

// Create is called in response to a create event.
func (w *workloadQueueHandler) Create(ctx context.Context, ev event.CreateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if cq, isCq := ev.Object.(*kueue.ClusterQueue); isCq {
		log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(cq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfClusterQueue(ctx, cq.Name, wq)
		return
	}
	if lq, isLq := ev.Object.(*kueue.LocalQueue); isLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(lq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, lq, wq)
	}
}

// Update is called in response to an update event.
func (w *workloadQueueHandler) Update(ctx context.Context, ev event.UpdateEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldCq, oldIsCq := ev.ObjectOld.(*kueue.ClusterQueue)
	newCq, newIsCq := ev.ObjectNew.(*kueue.ClusterQueue)
	if oldIsCq && newIsCq {
		log := ctrl.LoggerFrom(ctx).WithValues("clusterQueue", klog.KObj(ev.ObjectNew))
		ctx = ctrl.LoggerInto(ctx, log)
		log.V(5).Info("Workload cluster queue update event")

		if !newCq.DeletionTimestamp.IsZero() ||
			!gocmp.Equal(oldCq.Spec.AdmissionChecksStrategy, newCq.Spec.AdmissionChecksStrategy) ||
			!ptr.Equal(oldCq.Spec.StopPolicy, newCq.Spec.StopPolicy) {
			w.queueReconcileForWorkloadsOfClusterQueue(ctx, newCq.Name, wq)
		}
		return
	}

	oldLq, oldIsLq := ev.ObjectOld.(*kueue.LocalQueue)
	newLq, newIsLq := ev.ObjectNew.(*kueue.LocalQueue)
	if oldIsLq && newIsLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(ev.ObjectNew))
		ctx = ctrl.LoggerInto(ctx, log)
		log.V(5).Info("Workload cluster queue update event")

		if !newLq.DeletionTimestamp.IsZero() || !ptr.Equal(oldLq.Spec.StopPolicy, newLq.Spec.StopPolicy) {
			w.queueReconcileForWorkloadsOfLocalQueue(ctx, newLq, wq)
		}
	}
}

// Delete is called in response to a delete event.
func (w *workloadQueueHandler) Delete(ctx context.Context, ev event.DeleteEvent, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if cq, isCq := ev.Object.(*kueue.ClusterQueue); isCq {
		w.queueReconcileForWorkloadsOfClusterQueue(ctx, cq.Name, wq)
		return
	}
	if lq, isLq := ev.Object.(*kueue.LocalQueue); isLq {
		log := ctrl.LoggerFrom(ctx).WithValues("localQueue", klog.KObj(lq))
		ctx = ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, lq, wq)
	}
}

// Generic is called in response to an event of an unknown type or a synthetic event triggered as a cron or
// external trigger request.
func (w *workloadQueueHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// nothing to do here
}

func (w *workloadQueueHandler) queueReconcileForWorkloadsOfClusterQueue(ctx context.Context, cqName string, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.LocalQueueList{}
	err := w.r.client.List(ctx, &lst, client.MatchingFields{indexer.QueueClusterQueueKey: cqName})
	if err != nil {
		log.Error(err, "Could not list cluster queues local queues")
	}
	for _, lq := range lst.Items {
		log := log.WithValues("localQueue", klog.KObj(&lq))
		ctx := ctrl.LoggerInto(ctx, log)
		w.queueReconcileForWorkloadsOfLocalQueue(ctx, &lq, wq)
	}
}

func (w *workloadQueueHandler) queueReconcileForWorkloadsOfLocalQueue(ctx context.Context, lq *kueue.LocalQueue, wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx)
	lst := kueue.WorkloadList{}
	err := w.r.client.List(ctx, &lst, &client.ListOptions{Namespace: lq.Namespace}, client.MatchingFields{indexer.WorkloadQueueKey: lq.Name})
	if err != nil {
		log.Error(err, "Could not list cluster queues workloads")
	}
	for _, wl := range lst.Items {
		log := log.WithValues("workload", klog.KObj(&wl))
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      wl.Name,
				Namespace: wl.Namespace,
			},
		}
		wq.Add(req)
		log.V(5).Info("Queued reconcile for workload")
	}
}

type draEventHandler struct{}

var _ handler.TypedEventHandler[*kueue.Workload, reconcile.Request] = (*draEventHandler)(nil)

func (h *draEventHandler) Create(ctx context.Context, e event.TypedCreateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Update(ctx context.Context, e event.TypedUpdateEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *draEventHandler) Generic(ctx context.Context, e event.TypedGenericEvent[*kueue.Workload], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	reconcileReq := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      e.Object.Name,
			Namespace: e.Object.Namespace,
		},
	}
	q.Add(reconcileReq)
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(e.Object))
	log.V(4).Info("Queued reconcile for DRA workload from event channel")
}

// GetDRAReconcileChannel returns the DRA reconcile channel for connecting to the queue manager.
func (r *WorkloadReconciler) GetDRAReconcileChannel() chan<- event.TypedGenericEvent[*kueue.Workload] {
	return r.draReconcileChannel
}

type deviceClassHandler struct {
	r *WorkloadReconciler
}

func (h *deviceClassHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	dc := e.Object.(*resourcev1.DeviceClass)
	if ern := extendedResourceName(dc); ern != "" {
		h.r.draBackedResources.Add(corev1.ResourceName(ern), dc.Name)
		h.reconcileWorkloads(ctx, q, ern)
	}
}

func (h *deviceClassHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldDC := e.ObjectOld.(*resourcev1.DeviceClass)
	newDC := e.ObjectNew.(*resourcev1.DeviceClass)
	if oldERN := extendedResourceName(oldDC); oldERN != "" {
		h.r.draBackedResources.Remove(corev1.ResourceName(oldERN), oldDC.Name)
	}
	if newERN := extendedResourceName(newDC); newERN != "" {
		h.r.draBackedResources.Add(corev1.ResourceName(newERN), newDC.Name)
	}
	h.reconcileWorkloads(ctx, q, extendedResourceName(oldDC), extendedResourceName(newDC))
}

func (h *deviceClassHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	dc := e.Object.(*resourcev1.DeviceClass)
	if ern := extendedResourceName(dc); ern != "" {
		h.r.draBackedResources.Remove(corev1.ResourceName(ern), dc.Name)
		h.reconcileWorkloads(ctx, q, ern)
	}
}

func (h *deviceClassHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func extendedResourceName(dc *resourcev1.DeviceClass) string {
	if dc.Spec.ExtendedResourceName != nil {
		return *dc.Spec.ExtendedResourceName
	}
	return ""
}

func (h *deviceClassHandler) reconcileWorkloads(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], resourceNames ...string) {
	log := h.r.logger()

	// Requeue only workloads that request the affected extended resources.
	for _, name := range resourceNames {
		if name == "" {
			continue
		}
		lst := kueue.WorkloadList{}
		err := h.r.client.List(ctx, &lst,
			client.MatchingFields{
				indexer.WorkloadExtendedResourceKey: name,
				indexer.WorkloadQuotaReservedKey:    string(metav1.ConditionFalse),
			},
		)
		if err != nil {
			log.Error(err, "Could not list workloads for extended resource", "resource", name)
			continue
		}
		for i := range lst.Items {
			w := &lst.Items[i]
			log.V(3).Info("Requeuing workload due to DeviceClass change", "workload", klog.KObj(w), "resource", name)
			if !dra.NeedsDRAReconcile(w, h.r.draBackedResources) && workload.IsAdmissible(w) {
				wlCopy := w.DeepCopy()
				workload.AdjustResources(ctx, h.r.client, wlCopy)
				if err := h.r.queues.AddOrUpdateWorkload(log, wlCopy); err != nil {
					log.Error(err, "Failed to re-add workload to queue after DeviceClass change")
				}
			}
			q.AddAfter(reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      w.Name,
					Namespace: w.Namespace,
				},
			}, time.Second)
		}
	}
}

func (r *WorkloadReconciler) syncQuotaReservedFalseCondition(
	ctx context.Context,
	wl *kueue.Workload,
	lqExists, lqActive bool,
	cq *kueue.ClusterQueue,
) (bool, error) {
	var cond *metav1.Condition
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		reason, message, err := r.resolveGranularUnadmittedQuotaReservedCondition(ctx, wl, lqExists, lqActive, cq)
		if err != nil {
			return false, err
		}
		if reason != "" {
			cond = r.newQuotaReservedCondition(wl, reason, message)
		}
	} else {
		cond = r.resolveLegacyUnadmittedQuotaReservedCondition(wl, lqExists, lqActive, cq)
	}

	if cond != nil {
		return apimeta.SetStatusCondition(&wl.Status.Conditions, *cond), nil
	}
	return false, nil
}

func (r *WorkloadReconciler) resolveGranularUnadmittedQuotaReservedCondition(
	ctx context.Context,
	wl *kueue.Workload,
	lqExists, lqActive bool,
	cq *kueue.ClusterQueue,
) (string, string, error) {
	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)

	switch {
	case !workload.IsActive(wl):
		return kueue.WorkloadDeactivated, "The workload is deactivated", nil
	case workload.IsOnHold(wl):
		return cond.Reason, cond.Message, nil
	}

	if reason, message := r.getQueueBlocker(wl, lqExists, lqActive, cq); reason != "" {
		return reason, message, nil
	}

	var admissibilityErr error
	if cq != nil {
		var selector labels.Selector
		var err error
		selector, err = metav1.LabelSelectorAsSelector(cq.Spec.NamespaceSelector)
		if err != nil {
			log := ctrl.LoggerFrom(ctx)
			log.Error(err, "Invalid ClusterQueue NamespaceSelector", "clusterQueue", cq.Name)
			return kueue.WorkloadQuotaReservedReasonMisconfigured, fmt.Sprintf("invalid namespace selector: %v", err), nil
		}
		wlInfo := workload.NewInfo(wl)
		admissibilityErr = workload.ValidateAdmissibility(ctx, r.client, wlInfo, selector)
		if admissibilityErr != nil && errors.Is(admissibilityErr, workload.ErrInternal) {
			return "", "", admissibilityErr
		}
	}
	switch {
	case admissibilityErr != nil:
		return kueue.WorkloadQuotaReservedReasonMisconfigured, admissibilityErr.Error(), nil //nolint:nilerr // admissibility validation failure does not require retry
	case workload.HasAdmissionGate(wl):
		return kueue.WorkloadAdmissionGated, fmt.Sprintf("Admission is gated by: %s", wl.Annotations[constants.AdmissionGatedByAnnotation]), nil
	}

	if shouldCheckEquivalenceHash(cond) {
		if reason, ok := r.queues.GetNoFitReason(wl); ok {
			return reason, "Bypassed scheduling evaluation because an equivalent workload recently failed", nil
		}
	}

	switch {
	case cond != nil && cond.Reason != "" && schedulerSetReasons.Has(cond.Reason):
		// Preserve scheduler feedback reasons until the next scheduler cycle.
		return cond.Reason, cond.Message, nil
	default:
		// Stamping the condition on creation is gated by UnadmittedWorkloadsExplicitStatus.
		if cond == nil && !features.Enabled(features.UnadmittedWorkloadsExplicitStatus) {
			return "", "", nil
		}
		return kueue.WorkloadQuotaReservedReasonPendingEvaluation, "Workload is pending evaluation in the scheduling queue", nil
	}
}

func (r *WorkloadReconciler) resolveLegacyUnadmittedQuotaReservedCondition(wl *kueue.Workload, lqExists, lqActive bool, cq *kueue.ClusterQueue) *metav1.Condition {
	if _, message := r.getQueueBlocker(wl, lqExists, lqActive, cq); message != "" {
		return r.newQuotaReservedCondition(wl, kueue.WorkloadInadmissible, message)
	}
	return nil
}

func (r *WorkloadReconciler) newQuotaReservedCondition(wl *kueue.Workload, reason, message string) *metav1.Condition {
	return &metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            api.TruncateConditionMessage(message),
		LastTransitionTime: metav1.NewTime(r.clock.Now()),
		ObservedGeneration: wl.Generation,
	}
}

func (r *WorkloadReconciler) getQueueBlocker(wl *kueue.Workload, lqExists, lqActive bool, cq *kueue.ClusterQueue) (reason string, message string) {
	switch {
	case !lqExists:
		return kueue.WorkloadQuotaReservedReasonMisconfigured, fmt.Sprintf("LocalQueue %s doesn't exist", wl.Spec.QueueName)
	case !lqActive:
		return kueue.WorkloadQuotaReservedReasonSuspended, fmt.Sprintf("LocalQueue %s is inactive", wl.Spec.QueueName)
	case cq == nil:
		cqName, _ := r.queues.ClusterQueueForWorkload(wl)
		return kueue.WorkloadQuotaReservedReasonMisconfigured, fmt.Sprintf("ClusterQueue %s doesn't exist", cqName)
	case !r.cache.ClusterQueueActive(kueue.ClusterQueueReference(cq.Name)):
		return kueue.WorkloadQuotaReservedReasonSuspended, fmt.Sprintf("ClusterQueue %s is inactive", cq.Name)
	default:
		return "", ""
	}
}

// shouldCheckEquivalenceHash returns true if the workload is eligible to be
// checked against the scheduling equivalence cache.
func shouldCheckEquivalenceHash(cond *metav1.Condition) bool {
	if !features.Enabled(features.SchedulingEquivalenceHashing) {
		return false
	}
	// For newly created workloads (cond == nil), checking the equivalence cache
	// on creation is gated by UnadmittedWorkloadsExplicitStatus.
	if cond == nil {
		return features.Enabled(features.UnadmittedWorkloadsExplicitStatus)
	}
	if cond.Reason == "" {
		return true
	}
	// We only query the equivalence cache for workloads that are pending scheduling
	// or transitioning back to the queue (stale states).
	// We explicitly exclude:
	// - Statically blocked states (Misconfigured, AdmissionGated): We want to keep
	//   these blocking reasons visible instead of hiding them behind a bypassed message.
	// - Scheduler diagnostics (WaitingForQuota, etc.): We want to preserve the
	//   detailed resource breakdown messages set by the scheduler.
	//
	// This leaves only:
	// - PendingEvaluation / Pending (legacy): The workload is actively in the queue waiting for scheduler.
	// - Deactivated / Suspended: The workload was blocked but is now transitioning
	//   back to active scheduling (the previous block is resolved, so we check the cache).
	switch cond.Reason {
	case kueue.WorkloadDeactivated,
		kueue.WorkloadQuotaReservedReasonPendingEvaluation,
		kueue.WorkloadQuotaReservedReasonSuspended,
		kueue.WorkloadPending: //nolint:staticcheck // SA1019: legacy reason
		return true
	default:
		return false
	}
}
