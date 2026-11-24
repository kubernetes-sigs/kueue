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

package leaderworkerset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobframeworktas "sigs.k8s.io/kueue/pkg/controller/jobframework/tas"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utilstatefulset "sigs.k8s.io/kueue/pkg/util/statefulset"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltolerations "sigs.k8s.io/kueue/pkg/util/tolerations"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
)

const (
	leaderPodSetName = "leader"
	workerPodSetName = "worker"
	lwsDomainPrefix  = "leaderworkerset.sigs.k8s.io"
	lwsNameLabel     = "leaderworkerset.sigs.k8s.io/name"
)

type workloadToCreate struct {
	name  string
	index int
}

type Reconciler struct {
	integrationManager           *jobframework.IntegrationManager
	queues                       *qcache.Manager
	client                       client.Client
	apiReader                    client.Reader
	logName                      string
	record                       events.EventRecorder
	labelKeysToCopy              sets.Set[string]
	annotationsToCopy            sets.Set[string]
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
	customLabels                 *metrics.CustomLabels
}

const controllerName = "leaderworkerset"

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, eventRecorder events.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		integrationManager:           options.IntegrationManager,
		queues:                       options.Queues,
		client:                       client,
		apiReader:                    client,
		logName:                      "leaderworkerset-reconciler",
		record:                       eventRecorder,
		labelKeysToCopy:              options.LabelKeysToCopy,
		annotationsToCopy:            options.AnnotationsToCopy,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		roleTracker:                  options.RoleTracker,
		customLabels:                 options.CustomLabels,
	}, nil
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	mgr.GetLogger().WithName(r.logName).V(1).Info("Setting up LeaderWorkerSet reconciler")
	r.apiReader = mgr.GetAPIReader()

	return ctrl.NewControllerManagedBy(mgr).
		For(&leaderworkersetv1.LeaderWorkerSet{}, builder.WithPredicates(r)).
		Named(controllerName).
		Owns(&appsv1.StatefulSet{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&appsv1.StatefulSet{}, handler.EnqueueRequestsFromMapFunc(workerStatefulSetRequests),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.ControllerRevision{}, builder.OnlyMetadata).
		Watches(&kueue.Workload{}, &lwsWorkloadHandler{}).
		Watches(&corev1.Pod{}, &lwsPodHandler{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, "leaderworkerset-reconciler"),
		}).
		Complete(r)
}

func workerStatefulSetRequests(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[leaderworkersetv1.SetNameLabelKey]
	if name == "" || workerStatefulSetGroup(obj) == "" {
		return nil
	}
	return []reconcile.Request{{Namespace: obj.GetNamespace(), Name: name}}
}

func workerStatefulSetGroup(obj client.Object) string {
	owner := metav1.GetControllerOfNoCopy(obj)
	if owner == nil || owner.APIVersion != "v1" || owner.Kind != "Pod" {
		return ""
	}
	return obj.GetLabels()[leaderworkersetv1.GroupIndexLabelKey]
}

// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Reconcile LeaderWorkerSet")

	lws, err := r.getLeaderWorkerSet(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Child object watches bypass the LeaderWorkerSet event predicate.
	if lws != nil {
		managed, err := r.shouldManage(ctx, lws)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !managed {
			return ctrl.Result{}, nil
		}
	}

	statefulSets, err := r.getStatefulSets(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	pods, err := r.getPods(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	// If the LeaderWorkerSet is deleted, there is nothing to do with the workloads.
	if lws != nil {
		err = r.reconcileWorkloads(ctx, lws, pods)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile Pods only after reconciling workloads to ensure all workloads are recreated if needed.
	err = r.reconcilePods(ctx, lws, statefulSets, pods)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) getLeaderWorkerSet(ctx context.Context, req reconcile.Request) (*leaderworkersetv1.LeaderWorkerSet, error) {
	lws := &leaderworkersetv1.LeaderWorkerSet{}
	if err := r.client.Get(ctx, req.NamespacedName, lws); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return nil, fmt.Errorf("getting LeaderWorkerSet: %w", err)
		}
		return nil, nil
	}
	return lws, nil
}

func (r *Reconciler) getStatefulSets(ctx context.Context, req reconcile.Request) ([]appsv1.StatefulSet, error) {
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.client.List(ctx, statefulSets, client.InNamespace(req.Namespace),
		client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: req.Name},
	); err != nil {
		return nil, fmt.Errorf("listing StatefulSets: %w", err)
	}
	return statefulSets.Items, nil
}

func (r *Reconciler) getPods(ctx context.Context, req reconcile.Request) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	err := r.client.List(ctx, pods, client.InNamespace(req.Namespace),
		client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: req.Name},
	)
	if err != nil {
		return nil, fmt.Errorf("listing Pods: %w", err)
	}
	return pods.Items, nil
}

func (r *Reconciler) reconcileWorkloads(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, pods []corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(4).Info("Reconcile LeaderWorkerSet Workloads")

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(lws.GetNamespace()),
		client.MatchingFields{indexer.OwnerReferenceUID: string(lws.GetUID())},
	); err != nil {
		return fmt.Errorf("listing Workloads: %w", err)
	}

	toCreate, toUpdate, toDelete, err := r.filterWorkloads(lws, wlList.Items)
	if err != nil {
		return err
	}

	// The branches hold disjoint sets of Workloads, so one failing is no reason
	// to abandon the others. A derived context would cancel them, and
	// parallelize.Until checks for cancellation before each item, so a delete
	// that failed first could stop the other branches before they ran at all.
	// The reconcile context still carries shutdown and any deadline.
	var eg errgroup.Group

	eg.Go(func() error {
		var sts *appsv1.StatefulSet
		partition := rollingUpdatePartition(fromObject(lws))
		if len(toCreate) > 0 && features.Enabled(features.TopologyAwareScheduling) && lws.Spec.GroupIdentity != leaderworkersetv1.GroupIdentityHash {
			var err error
			sts, err = r.getLeaderStatefulSet(ctx, lws)
			if err != nil {
				return err
			}
			if sts != nil && metav1.IsControlledBy(sts, lws) {
				partition = max(partition, statefulSetPartition(sts))
			} else {
				sts = nil
			}
		}
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			group := toCreate[i]
			var expectedPodSets []kueue.PodSet
			var err error
			if sts != nil && group.index < int(partition) {
				expectedPodSets, err = r.podSetsForLeaderRevision(ctx, lws, sts, group.index)
			} else {
				expectedPodSets, err = podSets(lws)
			}
			if err != nil {
				return fmt.Errorf("building PodSets for Workload %s: %w", klog.KRef(lws.Namespace, group.name), err)
			}
			if len(expectedPodSets) == 0 {
				return nil
			}
			return r.createWorkload(ctx, lws, group.name, group.index, expectedPodSets)
		})
	})

	eg.Go(func() error {
		// One call resolves the class once, so every component moving to it gets
		// the same value. It goes first: a refused update stays on the in-memory
		// Workload, and a later call would resend it, fail, and skip the rest.
		priorityErr := jobframework.UpdateWorkloadPriority(ctx, r.client, r.record, lws, nil, toUpdate...)
		if priorityErr != nil {
			log.Error(priorityErr, "Failed to update Workload priority")
		}
		var topologyUpdates map[string][]topologyRequestUpdate
		var protectedWorkloads sets.Set[string]
		partition := rollingUpdatePartition(fromObject(lws))
		_, isMultiKueueRemote := lws.Labels[kueue.MultiKueueOriginLabel]
		if features.Enabled(features.TopologyAwareScheduling) && !isMultiKueueRemote && lws.DeletionTimestamp.IsZero() &&
			lws.Spec.GroupIdentity != leaderworkersetv1.GroupIdentityHash &&
			slices.ContainsFunc(toUpdate, needsTopologyCheck) {
			desired, err := podSets(lws)
			if err != nil {
				return errors.Join(priorityErr, fmt.Errorf("building LeaderWorkerSet PodSets: %w", err))
			}
			topologyUpdates = make(map[string][]topologyRequestUpdate)
			for _, wl := range toUpdate {
				if !needsTopologyCheck(wl) {
					continue
				}
				if updates := changedTopologyRequests(wl.Spec.PodSets, desired, workload.HasQuotaReservation(wl)); len(updates) > 0 {
					topologyUpdates[wl.Name] = updates
				}
			}
			if len(topologyUpdates) > 0 {
				rollout, err := r.appliedTopologyRollout(ctx, lws, desired)
				if err != nil {
					return errors.Join(priorityErr, err)
				}
				if rollout != nil {
					partition = max(partition, rollout.partition)
					protectedWorkloads = workloadsWithActivePods(lws, pods)
					if err := r.protectStaleWorkerWorkloads(ctx, lws, rollout, desired[len(desired)-1], protectedWorkloads); err != nil {
						return errors.Join(priorityErr, err)
					}
				} else {
					topologyUpdates = nil
				}
			}
		}
		updateErr := parallelize.Until(ctx, len(toUpdate), func(i int) error {
			wl := toUpdate[i]
			updates := topologyUpdates[wl.Name]
			if len(updates) > 0 {
				if protectedWorkloads.Has(wl.Name) || !usesCurrentTemplate(wl, partition) {
					updates = nil
				} else if workload.HasQuotaReservation(wl) {
					return r.deleteWorkloadWithStaleTopology(ctx, lws, wl)
				}
			}
			return r.updateWorkload(ctx, lws, wl, updates)
		})
		return errors.Join(priorityErr, updateErr)
	})

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toDelete), func(i int) error {
			return r.deleteWorkload(ctx, toDelete[i])
		})
	})

	return eg.Wait()
}

// filterWorkloads compares the desired state of a LeaderWorkerSet with existing workloads,
// determining which workloads need to be created, updated, or deleted.
//
// It accepts a LeaderWorkerSet and a slice of existing Workload objects as input and returns:
// 1. A slice of workloads to be created (with name and index)
// 2. A slice of workloads that may require updates
// 3. A slice of Workload pointers to be deleted
//
// During rolling updates with maxSurge, status.Replicas may temporarily exceed spec.Replicas.
// This function ensures workloads exist for all groups including surge replicas.
func (r *Reconciler) filterWorkloads(lws *leaderworkersetv1.LeaderWorkerSet, existingWorkloads []kueue.Workload) ([]workloadToCreate, []*kueue.Workload, []*kueue.Workload, error) {
	var (
		toCreate []workloadToCreate
		toUpdate []*kueue.Workload
		toDelete = utilslices.ToRefMap(existingWorkloads, func(e *kueue.Workload) string {
			return e.Name
		})
		replicas = ptr.Deref(lws.Spec.Replicas, defaultLeaderWorkerSetReplicas)
	)

	// During normal scale-down, status.Replicas lags behind spec.Replicas,
	// which prevents excess workloads from being moved to toDelete on time.
	if lws.Status.Replicas > replicas {
		surging, err := isRollingUpdateWithSurge(lws)
		if err != nil {
			return nil, nil, nil, err
		}
		if surging {
			replicas = lws.Status.Replicas
		}
	}

	_, isMultiKueueRemote := lws.Labels[kueue.MultiKueueOriginLabel]

	ownerUID := GetOwnerUID(lws)
	for i := range replicas {
		workloadName := GetWorkloadName(ownerUID, lws.Name, fmt.Sprint(i))
		if wl, ok := toDelete[workloadName]; ok {
			toUpdate = append(toUpdate, wl)
			delete(toDelete, workloadName)
		} else if !isMultiKueueRemote {
			toCreate = append(toCreate, workloadToCreate{name: workloadName, index: int(i)})
		}
	}

	return toCreate, toUpdate, slices.Collect(maps.Values(toDelete)), nil
}

// isRollingUpdateWithSurge reports whether the LeaderWorkerSet is rolling out
// with room for surge replicas. A percentage maxSurge is scaled against the
// replicas the update started from, rounding up as the LeaderWorkerSet API does.
func isRollingUpdateWithSurge(lws *leaderworkersetv1.LeaderWorkerSet) (bool, error) {
	if lws.Spec.RolloutStrategy.RollingUpdateConfiguration == nil {
		return false, nil
	}
	replicas := ptr.Deref(lws.Spec.Replicas, defaultLeaderWorkerSetReplicas)
	maxSurge, err := intstr.GetScaledValueFromIntOrPercent(&lws.Spec.RolloutStrategy.RollingUpdateConfiguration.MaxSurge, int(replicas), true)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errInvalidMaxSurge, err)
	}
	return maxSurge > 0 && lws.Status.UpdatedReplicas < replicas, nil
}

func (r *Reconciler) createWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int, podSets []kueue.PodSet) error {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"workload", klog.ObjectRef{Name: workloadName, Namespace: lws.Namespace},
		"index", index,
	)

	createdWorkload, err := r.constructWorkload(lws, workloadName, index, podSets)
	if err != nil {
		log.Error(err, "Failed to construct Workload")
		return fmt.Errorf("constructing Workload %s: %w", klog.KRef(lws.Namespace, workloadName), err)
	}

	err = jobframework.PrepareWorkloadPriority(ctx, r.client, r.record, lws, createdWorkload, nil)
	if err != nil {
		log.Error(err, "Failed to prepare Workload priority")
		return fmt.Errorf("preparing priority of Workload %s: %w", klog.KRef(lws.Namespace, workloadName), err)
	}

	err = r.client.Create(ctx, createdWorkload)
	if apierrors.IsAlreadyExists(err) {
		log.V(5).Info("Workload already exists but is not in the cache yet")
		return nil
	}
	if err != nil {
		log.Error(err, "Failed to create Workload")
		return fmt.Errorf("creating Workload %s: %w", klog.KRef(lws.Namespace, workloadName), err)
	}
	r.record.Eventf(
		lws, nil, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload,
		"CreatedWorkload",
		"Created Workload: %v", workload.Key(createdWorkload),
	)

	jobframework.RecordWorkloadCreationLatency(ctx, lws, lws.GroupVersionKind().Kind, createdWorkload, r.queues, r.customLabels, r.roleTracker)

	return nil
}

func (r *Reconciler) constructWorkload(lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int, podSets []kueue.PodSet) (*kueue.Workload, error) {
	createdWorkload := podcontroller.NewGroupWorkload(workloadName, lws, podSets, r.labelKeysToCopy, r.annotationsToCopy)

	if createdWorkload.Labels == nil {
		createdWorkload.Labels = make(map[string]string, 1)
	}
	createdWorkload.Labels[controllerconstants.JobUIDLabel] = string(lws.UID)

	// Add job owner annotations for reliable MultiKueue adapter lookup.
	// These annotations persist even after Kubernetes GC removes owner references.
	if createdWorkload.Annotations == nil {
		createdWorkload.Annotations = make(map[string]string)
	}
	createdWorkload.Annotations[controllerconstants.JobOwnerGVKAnnotation] = gvk.String()
	createdWorkload.Annotations[controllerconstants.JobOwnerNameAnnotation] = lws.Name
	createdWorkload.Annotations[controllerconstants.ComponentWorkloadIndexAnnotation] = strconv.Itoa(index)

	if features.Enabled(features.AdmissionGatedBy) {
		jobframework.PropagateAdmissionGatedByAnnotation(lws, createdWorkload)
	}

	if err := controllerutil.SetOwnerReference(lws, createdWorkload, r.client.Scheme()); err != nil {
		return nil, err
	}
	return createdWorkload, nil
}

func dropLWSPrefixedKeys(m map[string]string) {
	for k := range m {
		if strings.HasPrefix(k, lwsDomainPrefix) && k != lwsNameLabel {
			delete(m, k)
		}
	}
}

func newPodSet(name kueue.PodSetReference, count int32, template *corev1.PodTemplateSpec, podIndexLabel *string) (*kueue.PodSet, error) {
	podSet := &kueue.PodSet{
		Name:     name,
		Count:    count,
		Template: *template.DeepCopy(),
	}
	dropLWSPrefixedKeys(podSet.Template.Labels)
	dropLWSPrefixedKeys(podSet.Template.Annotations)
	jobframework.SanitizePodSet(podSet)
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframeworktas.NewPodSetTopologyRequest(template.ObjectMeta.DeepCopy()).
			PodIndexLabel(podIndexLabel).
			Build()
		if err != nil {
			return nil, err
		}
		podSet.TopologyRequest = topologyRequest
	}
	return podSet, nil
}

func podSets(lws *leaderworkersetv1.LeaderWorkerSet) ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0, 2)

	defaultPodSetName := kueue.DefaultPodSetName
	defaultPodSetCount := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, defaultLeaderWorkerSetSize)

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		defaultPodSetName = workerPodSetName
		defaultPodSetCount--

		leaderPodSet, err := newPodSet(leaderPodSetName, 1, lws.Spec.LeaderWorkerTemplate.LeaderTemplate, nil)
		if err != nil {
			return nil, err
		}

		podSets = append(podSets, *leaderPodSet)
	}

	workerPodSet, err := newPodSet(
		defaultPodSetName,
		defaultPodSetCount,
		&lws.Spec.LeaderWorkerTemplate.WorkerTemplate,
		new(leaderworkersetv1.WorkerIndexLabelKey),
	)
	if err != nil {
		return nil, err
	}

	podSets = append(podSets, *workerPodSet)

	return podSets, nil
}

func needsTopologyCheck(wl *kueue.Workload) bool {
	hasReservation := workload.HasQuotaReservation(wl)
	canRefresh := !hasReservation && wl.Status.Admission == nil
	canReplace := hasReservation && len(wl.Status.AdmissionChecks) > 0
	return wl.DeletionTimestamp.IsZero() && workload.IsActive(wl) && !workloadfinish.IsFinished(wl) &&
		wl.Annotations[podconstants.IsGroupWorkloadAnnotationKey] == podconstants.IsGroupWorkloadAnnotationValue &&
		!workload.IsAdmitted(wl) && !workloadevict.IsEvicted(wl) &&
		!workload.HasRetryChecks(wl) && !workload.HasRejectedChecks(wl) && (canRefresh || canReplace)
}

type appliedTopologyRollout struct {
	partition            int32
	revision             string
	leaderStatefulSetUID types.UID
}

func (r *Reconciler) appliedTopologyRollout(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, desired []kueue.PodSet) (*appliedTopologyRollout, error) {
	if lws.Spec.Scheduling != nil {
		condition := apimeta.FindStatusCondition(lws.Status.Conditions, string(leaderworkersetv1.LeaderWorkerSetWorkloadSchedulingCreated))
		if condition == nil || condition.ObservedGeneration != lws.Generation || condition.Status != metav1.ConditionTrue {
			return nil, nil
		}
	}
	// LeaderWorkerSet status and StatefulSet updates can reach separate caches
	// in different orders. Read the leader StatefulSet's effective partition
	// directly before replacing reservations for groups in the rollout.
	sts, err := r.getLeaderStatefulSet(ctx, lws)
	if err != nil {
		return nil, err
	}
	if sts == nil {
		// Kueue can reserve quota before the LWS controller creates Pods.
		return &appliedTopologyRollout{}, nil
	}
	if !metav1.IsControlledBy(sts, lws) {
		return nil, nil
	}
	if lws.Status.ObservedGeneration < lws.Generation || !sts.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	// LWS can publish ObservedGeneration before successfully applying the new
	// template. Only a partition for the applied topology can release quota.
	if matches, err := r.appliedTopologyMatches(ctx, lws, sts, desired); !matches || err != nil {
		return nil, err
	}
	return &appliedTopologyRollout{
		partition:            statefulSetPartition(sts),
		revision:             sts.Spec.Template.Labels[leaderworkersetv1.RevisionKey],
		leaderStatefulSetUID: sts.UID,
	}, nil
}

func (r *Reconciler) getLeaderStatefulSet(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKeyFromObject(lws)
	if err := r.apiReader.Get(ctx, key, sts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting leader StatefulSet %s: %w", key, err)
	}
	return sts, nil
}

func statefulSetPartition(sts *appsv1.StatefulSet) int32 {
	if rollingUpdate := sts.Spec.UpdateStrategy.RollingUpdate; rollingUpdate != nil {
		return ptr.Deref(rollingUpdate.Partition, 0)
	}
	return 0
}

func (r *Reconciler) podSetsForLeaderRevision(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, sts *appsv1.StatefulSet, index int) ([]kueue.PodSet, error) {
	if !sts.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	// Below the partition a new Pod can use an older revision, even though
	// the StatefulSet template is already updated. Wait for the actual Pod
	// and its LWS revision; their watches will requeue this group.
	var leader corev1.Pod
	key := client.ObjectKey{Namespace: sts.Namespace, Name: fmt.Sprintf("%s-%d", sts.Name, index)}
	if err := r.apiReader.Get(ctx, key, &leader); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(&leader, sts) || !leader.DeletionTimestamp.IsZero() || utilpod.IsTerminated(&leader) {
		return nil, nil
	}
	revisionPodSets, err := r.podSetsForRevision(ctx, lws, leader.Labels[leaderworkersetv1.RevisionKey])
	if err != nil || len(revisionPodSets) == 0 {
		return nil, err
	}
	// If LWS reconstructed history under the same key, the running group
	// can still use its original topology. Prefer the actual Pod and, if
	// workers already exist, their controlled StatefulSet.
	leaderTopology, err := podSetTopologyFrom(&leader.ObjectMeta, revisionPodSets[0])
	if err != nil {
		return nil, err
	}
	workerSet := &revisionPodSets[len(revisionPodSets)-1]
	if workerSet.Count > 0 && (len(revisionPodSets) > 1 || workerSet.Count > 1) {
		var worker appsv1.StatefulSet
		err := r.apiReader.Get(ctx, key, &worker)
		if client.IgnoreNotFound(err) != nil {
			return nil, fmt.Errorf("getting worker StatefulSet %s: %w", key, err)
		}
		if err == nil && metav1.IsControlledBy(&worker, &leader) {
			if !worker.DeletionTimestamp.IsZero() {
				return nil, nil
			}
			workerSet.TopologyRequest, err = podSetTopologyFrom(&worker.Spec.Template.ObjectMeta, *workerSet)
			if err != nil {
				return nil, err
			}
		}
		if len(revisionPodSets) == 1 && !apiequality.Semantic.DeepEqual(leaderTopology, workerSet.TopologyRequest) {
			// One PodSet cannot describe two different topology requests.
			return nil, nil
		}
	}
	revisionPodSets[0].TopologyRequest = leaderTopology
	return revisionPodSets, nil
}

func (r *Reconciler) appliedTopologyMatches(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, sts *appsv1.StatefulSet, desired []kueue.PodSet) (bool, error) {
	// The leader's revision supplies the worker template, which is absent
	// from the StatefulSet when the roles use separate templates.
	appliedPodSets, err := r.podSetsForRevision(ctx, lws, sts.Spec.Template.Labels[leaderworkersetv1.RevisionKey])
	if err != nil {
		return false, err
	}
	if !slices.EqualFunc(appliedPodSets, desired, func(a, b kueue.PodSet) bool {
		return a.Name == b.Name && a.Count == b.Count && apiequality.Semantic.DeepEqual(a.TopologyRequest, b.TopologyRequest)
	}) {
		return false, nil
	}
	// LWS can recreate missing history using the live LWS template. Verify
	// the leader portion against the actual StatefulSet in that case too.
	return podTemplateTopologyMatches(&sts.Spec.Template, desired[0])
}

func (r *Reconciler) podSetsForRevision(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, key string) ([]kueue.PodSet, error) {
	if key == "" {
		return nil, nil
	}
	var revisions appsv1.ControllerRevisionList
	if err := r.apiReader.List(ctx, &revisions, client.InNamespace(lws.Namespace), client.MatchingLabels{
		leaderworkersetv1.SetNameLabelKey: lws.Name,
		leaderworkersetv1.RevisionKey:     key,
	}); err != nil {
		return nil, fmt.Errorf("listing LeaderWorkerSet revisions: %w", err)
	}
	var applied *appsv1.ControllerRevision
	for i := range revisions.Items {
		rev := &revisions.Items[i]
		if metav1.IsControlledBy(rev, lws) && (applied == nil || rev.Revision > applied.Revision) {
			applied = rev
		}
	}
	if applied == nil {
		return nil, nil
	}
	var snapshot struct {
		Spec struct {
			Template *leaderworkersetv1.LeaderWorkerTemplate `json:"leaderWorkerTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(applied.Data.Raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decoding LeaderWorkerSet revision %q: %w", applied.Name, err)
	}
	if snapshot.Spec.Template == nil {
		return nil, nil
	}
	appliedPodSets, err := podSets(&leaderworkersetv1.LeaderWorkerSet{Spec: leaderworkersetv1.LeaderWorkerSetSpec{
		LeaderWorkerTemplate: *snapshot.Spec.Template,
	}})
	if err != nil {
		return nil, fmt.Errorf("building PodSets for LeaderWorkerSet revision %q: %w", applied.Name, err)
	}
	return appliedPodSets, nil
}

func podTemplateTopologyMatches(template *corev1.PodTemplateSpec, desired kueue.PodSet) (bool, error) {
	topology, err := podSetTopologyFrom(&template.ObjectMeta, desired)
	return apiequality.Semantic.DeepEqual(topology, desired.TopologyRequest), err
}

func podSetTopologyFrom(meta *metav1.ObjectMeta, desired kueue.PodSet) (*kueue.PodSetTopologyRequest, error) {
	var podIndexLabel *string
	if desired.TopologyRequest != nil {
		podIndexLabel = desired.TopologyRequest.PodIndexLabel
	}
	return jobframeworktas.NewPodSetTopologyRequest(meta).PodIndexLabel(podIndexLabel).Build()
}

func (r *Reconciler) protectStaleWorkerWorkloads(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, rollout *appliedTopologyRollout, desired kueue.PodSet, protected sets.Set[string]) error {
	if rollout.revision == "" || ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, defaultLeaderWorkerSetSize) <= 1 {
		return nil
	}
	var statefulSets appsv1.StatefulSetList
	if err := r.apiReader.List(ctx, &statefulSets, client.InNamespace(lws.Namespace), client.MatchingLabels{
		leaderworkersetv1.SetNameLabelKey: lws.Name,
		leaderworkersetv1.RevisionKey:     rollout.revision,
	}); err != nil {
		return fmt.Errorf("listing worker StatefulSets: %w", err)
	}
	for i := range statefulSets.Items {
		sts := &statefulSets.Items[i]
		group := workerStatefulSetGroup(sts)
		if group == "" {
			continue
		}
		// Recreating revision history under an old key changes the template
		// future workers will use. An existing worker StatefulSet keeps its
		// own template until it is replaced.
		matches, err := podTemplateTopologyMatches(&sts.Spec.Template, desired)
		if err == nil && matches {
			continue
		}
		owner := metav1.GetControllerOfNoCopy(sts)
		var leader corev1.Pod
		if err := r.apiReader.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: owner.Name}, &leader); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting leader Pod of worker StatefulSet %s: %w", klog.KObj(sts), err)
		}
		leaderOwner := metav1.GetControllerOfNoCopy(&leader)
		if leader.UID == owner.UID && leader.Labels[leaderworkersetv1.GroupIndexLabelKey] == group &&
			leader.Labels[leaderworkersetv1.RevisionKey] == rollout.revision &&
			leaderOwner != nil && leaderOwner.UID == rollout.leaderStatefulSetUID {
			protected.Insert(GetWorkloadName(lws.UID, lws.Name, group))
		}
	}
	return nil
}

func usesCurrentTemplate(wl *kueue.Workload, partition int32) bool {
	if partition == 0 {
		return true
	}
	// The groups below a rolling update's partition keep their previous template.
	index, err := strconv.ParseInt(wl.Annotations[controllerconstants.ComponentWorkloadIndexAnnotation], 10, 32)
	return err == nil && index >= int64(partition)
}

func workloadsWithActivePods(lws *leaderworkersetv1.LeaderWorkerSet, pods []corev1.Pod) sets.Set[string] {
	active := sets.New[string]()
	for i := range pods {
		pod := &pods[i]
		// A gated, unbound Pod cannot use the old reservation.
		if utilpod.IsTerminated(pod) ||
			(pod.Spec.NodeName == "" && pod.Status.Phase != corev1.PodRunning && utilpod.HasGate(pod, podconstants.SchedulingGateName)) {
			continue
		}
		if group := pod.Labels[leaderworkersetv1.GroupIndexLabelKey]; group != "" {
			active.Insert(GetWorkloadName(lws.UID, lws.Name, group))
		}
	}
	return active
}

type topologyRequestUpdate struct {
	index   int
	request *kueue.PodSetTopologyRequest
}

func changedTopologyRequests(current, desired []kueue.PodSet, reserved bool) []topologyRequestUpdate {
	// Only reconcile topology for the same group shape.
	if len(current) != len(desired) {
		return nil
	}
	var updates []topologyRequestUpdate
	for _, want := range desired {
		i := slices.IndexFunc(current, func(ps kueue.PodSet) bool {
			return ps.Name == want.Name && ps.Count == want.Count
		})
		if i < 0 {
			return nil
		}
		have := current[i].TopologyRequest
		// Leave unreserved PodSets without a placement request alone. The
		// desired spec can still contain derived indexing fields.
		if !reserved && !hasRequestedTopology(have) && !hasRequestedTopology(want.TopologyRequest) {
			continue
		}
		if !apiequality.Semantic.DeepEqual(have, want.TopologyRequest) {
			updates = append(updates, topologyRequestUpdate{index: i, request: want.TopologyRequest})
		}
	}
	return updates
}

func hasRequestedTopology(request *kueue.PodSetTopologyRequest) bool {
	return utiltas.HasTopologyConstraint(request) || (request != nil && request.PodSetGroupName != nil)
}

func (r *Reconciler) deleteWorkloadWithStaleTopology(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, wl *kueue.Workload) error {
	// Let the PodGroup controller finalize the old Workload. Keep the
	// preconditions so a concurrent admission or replacement survives.
	if err := r.client.Delete(ctx, wl, client.Preconditions{
		UID: &wl.UID, ResourceVersion: &wl.ResourceVersion,
	}); err != nil {
		return client.IgnoreNotFound(err)
	}
	r.record.Eventf(lws, nil, corev1.EventTypeNormal, jobframework.ReasonDeletedWorkload, "DeletedWorkload",
		"Deleted Workload %s because its reserved topology request no longer matches the LeaderWorkerSet template", workload.Key(wl))
	return nil
}

func (r *Reconciler) updateWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, wl *kueue.Workload, topologyUpdates []topologyRequestUpdate) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Update LeaderWorkerSet Workload")

	previousQueueName := wl.Spec.QueueName
	res := syncWorkload(ctx, lws, wl)
	if !res.updated && len(topologyUpdates) == 0 {
		return nil
	}
	for _, update := range topologyUpdates {
		wl.Spec.PodSets[update.index].TopologyRequest = update.request.DeepCopy()
	}
	// The Workload's resource version protects these changes if the scheduler
	// reserves quota between the list and the update.
	if err := r.client.Update(ctx, wl); err != nil {
		log.Error(err, "Failed to update Workload")
		return fmt.Errorf("updating Workload %s: %w", klog.KObj(wl), err)
	}
	if res.queueNameUpdated {
		log.V(3).Info("Updated Workload queue name",
			"previousLocalQueue", klog.KRef(wl.Namespace, string(previousQueueName)),
			"localQueue", klog.KRef(wl.Namespace, string(wl.Spec.QueueName)))
	}
	if res.tolerationsUpdated {
		log.V(3).Info("Updated Workload tolerations")
	}
	if res.admissionGatedByUpdated {
		jobframework.RecordAdmissionGatedByUpdateEvent(r.record, lws)
	}
	if res.waitForPodsReadyEvent != jobframework.WaitForPodsReadyEventNone {
		jobframework.RecordWaitForPodsReadyUpdateEvent(r.record, lws, wl, res.waitForPodsReadyEvent)
	}

	return nil
}

type workloadSyncResult struct {
	updated                 bool
	queueNameUpdated        bool
	tolerationsUpdated      bool
	admissionGatedByUpdated bool
	waitForPodsReadyEvent   jobframework.WaitForPodsReadyEvent
}

func syncWorkload(
	ctx context.Context,
	lws *leaderworkersetv1.LeaderWorkerSet,
	wl *kueue.Workload,
) workloadSyncResult {
	var res workloadSyncResult

	if queueName := jobframework.QueueNameForObject(lws); wl.Spec.QueueName != queueName {
		wl.Spec.QueueName = queueName
		res.queueNameUpdated = true
		res.updated = true
	}

	if syncTolerations(lws, wl) {
		res.tolerationsUpdated = true
		res.updated = true
	}

	if jobframework.PropagateAdmissionGatedByAnnotation(lws, wl) {
		res.admissionGatedByUpdated = true
		res.updated = true
	}

	if waitforpodsready.WorkloadLevelWaitForPodsReadyEnabled() {
		var waitForPodsReadyUpdated bool
		waitForPodsReadyUpdated, res.waitForPodsReadyEvent = jobframework.SyncWaitForPodsReady(ctx, lws, wl)
		if waitForPodsReadyUpdated {
			res.updated = true
		}
	}

	return res
}

// syncTolerations copies the pod template tolerations to the Workload PodSets
// and reports whether any PodSet changed. The webhook only allows adding
// tolerations, so a Workload that holds a quota reservation gets them as well:
// TAS checks node taints against them while the new pods run. The Workload on
// a MultiKueue worker is a copy of the one on the manager cluster, so it is
// left unchanged.
func syncTolerations(lws *leaderworkersetv1.LeaderWorkerSet, wl *kueue.Workload) bool {
	if !features.Enabled(features.AllowAddingPodTemplateTolerations) {
		return false
	}
	if _, isMultiKueueRemote := lws.Labels[kueue.MultiKueueOriginLabel]; isMultiKueueRemote {
		return false
	}
	return utiltolerations.CopyAddedByName(workload.PodTemplates(wl), podTemplates(lws))
}

// podTemplates returns the pod templates of the LeaderWorkerSet by the name of
// the PodSet that podSets builds from them.
func podTemplates(lws *leaderworkersetv1.LeaderWorkerSet) map[kueue.PodSetReference]*corev1.PodTemplateSpec {
	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate == nil {
		return map[kueue.PodSetReference]*corev1.PodTemplateSpec{
			kueue.DefaultPodSetName: &lws.Spec.LeaderWorkerTemplate.WorkerTemplate,
		}
	}
	return map[kueue.PodSetReference]*corev1.PodTemplateSpec{
		leaderPodSetName: lws.Spec.LeaderWorkerTemplate.LeaderTemplate,
		workerPodSetName: &lws.Spec.LeaderWorkerTemplate.WorkerTemplate,
	}
}

func (r *Reconciler) deleteWorkload(ctx context.Context, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))

	deleted, err := workload.Delete(ctx, r.client, wl)
	if err != nil {
		log.Error(err, "Failed to delete Workload")
		return fmt.Errorf("deleting Workload %s: %w", klog.KObj(wl), err)
	}
	if deleted {
		log.V(3).Info("Deleted LeaderWorkerSet Workload")
	}
	return nil
}

func (r *Reconciler) reconcilePods(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, statefulSets []appsv1.StatefulSet, pods []corev1.Pod) error {
	statefulSetsMap := utilslices.ToRefMap(statefulSets, func(e *appsv1.StatefulSet) string {
		return e.Name
	})

	return parallelize.Until(ctx, len(pods), func(i int) error {
		pod := &pods[i]
		var sts *appsv1.StatefulSet
		if ref := metav1.GetControllerOf(pod); ref != nil {
			sts = statefulSetsMap[ref.Name]
		}
		return r.reconcilePod(ctx, lws, sts, pod)
	})
}

func (r *Reconciler) reconcilePod(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, sts *appsv1.StatefulSet, pod *corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"pod", klog.KObj(pod),
		"podRevision", pod.Labels[appsv1.ControllerRevisionHashLabelKey],
		"group", utilpod.GetPodGroupName(pod))
	if sts != nil {
		log = log.WithValues(
			"statefulSet", klog.KObj(sts),
			"currentRevision", sts.Status.CurrentRevision,
			"updateRevision", sts.Status.UpdateRevision,
		)
	}
	log.V(5).Info("Reconcile LeaderWorkerSet Pod")

	// Kueue stops managing the Pods of a deleted LeaderWorkerSet or StatefulSet, so it releases them.
	if lws == nil || sts == nil {
		// A child can disappear while another integration still owns the Pod.
		if parent := pod.Annotations[podconstants.SuspendedByParentAnnotation]; parent != "" && parent != FrameworkName {
			return nil
		}
		var ungated bool
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			ungated = utilstatefulset.UngatePod(pod)
			return ungated, nil
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to ungate Pod")
			return fmt.Errorf("ungating Pod %s: %w", klog.KObj(pod), err)
		}
		if err == nil {
			if ungated {
				log.V(3).Info("Ungated LeaderWorkerSet Pod")
			} else {
				log.V(5).Info("Skipped ungating LeaderWorkerSet Pod")
			}
		}
	} else if !utilpod.IsTerminated(pod) && pod.DeletionTimestamp == nil {
		var updated bool
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated = r.setDefault(lws, pod)
			return updated, nil
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to set default values")
			return fmt.Errorf("setting default values on Pod %s: %w", klog.KObj(pod), err)
		}
		if err == nil && updated {
			log.V(3).Info("Set default values on LeaderWorkerSet Pod")
		}
	}

	return nil
}

func (r *Reconciler) setDefault(lws *leaderworkersetv1.LeaderWorkerSet, pod *corev1.Pod) bool {
	queueName := jobframework.QueueNameForObject(lws)

	if _, ok := pod.Labels[constants.ManagedByKueueLabelKey]; ok {
		// TODO(#13968): Candidate for removal once LeaderWorkerSets admitted before #4932
		// are gone, no earlier than 0.21. The webhook stamps the queue name on the pod
		// templates, so it is only missing here on those, which #4932 did not migrate.
		// Only gated pods qualify: the pod webhook rejects the change on others.
		if queueName != "" && utilpod.HasGate(pod, podconstants.SchedulingGateName) &&
			pod.Labels[controllerconstants.QueueLabel] != string(queueName) {
			pod.Labels[controllerconstants.QueueLabel] = string(queueName)
			return true
		}
		return false
	}

	// We should wait for GroupIndexLabelKey.
	if _, ok := pod.Labels[leaderworkersetv1.GroupIndexLabelKey]; !ok {
		return false
	}

	wlName := GetWorkloadName(GetOwnerUID(lws), lws.Name, pod.Labels[leaderworkersetv1.GroupIndexLabelKey])

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	// TODO(#13968): Candidate for removal together with the backfill above, no earlier
	// than 0.21. Normally the webhook has already stamped the queue name on the pod
	// template, so this only matters for a LeaderWorkerSet admitted before #4932;
	// setting it here saves reconciling the pod a second time to backfill it.
	// Only gated pods qualify: the pod webhook rejects the change on others.
	if queueName != "" && utilpod.HasGate(pod, podconstants.SchedulingGateName) {
		pod.Labels[controllerconstants.QueueLabel] = string(queueName)
	}
	podcontroller.SetPodGroupName(pod, wlName)
	jobframework.SetPrebuiltWorkloadName(pod, wlName)
	pod.Annotations[podconstants.GroupTotalCountAnnotation] = fmt.Sprint(ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, defaultLeaderWorkerSetSize))
	pod.Annotations[podconstants.RoleHashAnnotation] = string(kueue.DefaultPodSetName)

	if lws.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		if _, ok := pod.Annotations[leaderworkersetv1.LeaderPodNameAnnotationKey]; !ok {
			pod.Annotations[podconstants.RoleHashAnnotation] = leaderPodSetName
		} else {
			pod.Annotations[podconstants.RoleHashAnnotation] = workerPodSetName
		}
	}

	return true
}

var _ predicate.Predicate = (*Reconciler)(nil)

func (r *Reconciler) Generic(event.GenericEvent) bool {
	return false
}

func (r *Reconciler) Create(e event.CreateEvent) bool {
	return r.handle(e.Object)
}

func (r *Reconciler) Update(e event.UpdateEvent) bool {
	return r.handle(e.ObjectNew)
}

func (r *Reconciler) Delete(event.DeleteEvent) bool {
	return false
}

func (r *Reconciler) handle(obj client.Object) bool {
	lws, isLws := obj.(*leaderworkersetv1.LeaderWorkerSet)
	if !isLws {
		return false
	}

	log := r.logger().WithValues("leaderWorkerSet", klog.KObj(lws))
	ctx := ctrl.LoggerInto(context.Background(), log)

	managed, err := r.shouldManage(ctx, lws)
	if err != nil {
		log.Error(err, "Failed to determine if the LeaderWorkerSet should be managed by Kueue")
	}

	return managed
}

func (r *Reconciler) shouldManage(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet) (bool, error) {
	return r.integrationManager.WorkloadShouldBeSuspended(ctx, lws, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
}

// lwsWorkloadHandler watches for workload deletions and triggers reconciliation
// of the owning LeaderWorkerSet. This ensures that during rolling updates, when
// workloads are deleted, the LWS reconciler is triggered to recreate them.
type lwsWorkloadHandler struct{}

var _ handler.EventHandler = (*lwsWorkloadHandler)(nil)

func (h *lwsWorkloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *lwsWorkloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *lwsWorkloadHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *lwsWorkloadHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

// enqueue processes a workload object and enqueues a reconcile request for its owning LeaderWorkerSet if applicable.
func (h *lwsWorkloadHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(5).Info("Enqueue LeaderWorkerSet Workload")

	for _, ownerRef := range wl.OwnerReferences {
		if ownerRef.APIVersion == gvk.GroupVersion().String() && ownerRef.Kind == gvk.Kind {
			log.V(5).Info("Queueing reconcile for owning LeaderWorkerSet",
				"leaderWorkerSet", klog.KRef(wl.Namespace, ownerRef.Name),
			)
			q.AddAfter(
				reconcile.Request{
					Namespace: wl.Namespace,
					Name:      ownerRef.Name,
				},
				constants.UpdatesBatchPeriod,
			)
			return
		}
	}
}

// lwsPodHandler watches for Pod events and triggers reconciliation
// of the owning LeaderWorkerSet. This ensures that we will ungate Pods and set
// default values.
type lwsPodHandler struct{}

var _ handler.EventHandler = (*lwsPodHandler)(nil)

func (h *lwsPodHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *lwsPodHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *lwsPodHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if pod, ok := e.Object.(*corev1.Pod); ok && pod.Labels[leaderworkersetv1.GroupIndexLabelKey] != "" {
		h.enqueueLeaderWorkerSet(ctx, pod, q)
	}
}

func (h *lwsPodHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// enqueue processes the given Pod object and adds a reconcile request for the owning LeaderWorkerSet to the queue.
func (h *lwsPodHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))
	log.V(5).Info("Enqueue LeaderWorkerSet Pod")

	// Handle only Pods suspended by LeaderWorkerSet.
	if pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		log.V(5).Info("Pod is not suspended by parent")
		return
	}

	h.enqueueLeaderWorkerSet(ctx, pod, q)
}

func (h *lwsPodHandler) enqueueLeaderWorkerSet(ctx context.Context, pod *corev1.Pod, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))
	lwsName, ok := pod.Labels[leaderworkersetv1.SetNameLabelKey]
	if !ok {
		log.V(5).Info("Pod doesn't have LeaderWorkerSet name label")
		return
	}

	log.V(5).Info("Queueing reconcile for owning LeaderWorkerSet",
		"leaderWorkerSet", klog.KRef(pod.Namespace, lwsName),
	)

	q.AddAfter(
		reconcile.Request{
			Namespace: pod.Namespace,
			Name:      lwsName,
		},
		constants.UpdatesBatchPeriod,
	)
}
