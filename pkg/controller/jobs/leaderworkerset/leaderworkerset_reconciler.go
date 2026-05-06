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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
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
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utilstatefulset "sigs.k8s.io/kueue/pkg/util/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
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
	client                       client.Client
	logName                      string
	record                       events.EventRecorder
	labelKeysToCopy              []string
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
	customLabels                 *metrics.CustomLabels
}

const controllerName = "leaderworkerset"

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, eventRecorder events.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		logName:                      "leaderworkerset-reconciler",
		record:                       eventRecorder,
		labelKeysToCopy:              options.LabelKeysToCopy,
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
	ctrl.Log.V(3).Info("Setting up LeaderWorkerSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&leaderworkersetv1.LeaderWorkerSet{}, builder.WithPredicates(r)).
		Named(controllerName).
		Watches(&kueue.Workload{}, &lwsWorkloadHandler{}).
		Watches(&corev1.Pod{}, &lwsPodHandler{}).
		Watches(&appsv1.StatefulSet{}, &lwsStsHandler{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, controllerName),
		}).
		Complete(r)
}

// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets/status,verbs=get;patch;update

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet")

	lws, err := r.getLeaderWorkerSet(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
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
		err = r.reconcileWorkloads(ctx, lws)
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
	log := ctrl.LoggerFrom(ctx)
	lws := &leaderworkersetv1.LeaderWorkerSet{}
	if err := r.client.Get(ctx, req.NamespacedName, lws); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get LeaderWorkerSet")
			return nil, err
		}
		return nil, nil
	}
	return lws, nil
}

func (r *Reconciler) getStatefulSets(ctx context.Context, req reconcile.Request) ([]appsv1.StatefulSet, error) {
	log := ctrl.LoggerFrom(ctx)
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.client.List(ctx, statefulSets, client.InNamespace(req.Namespace),
		client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: req.Name},
	); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get StatefulSets")
			return nil, err
		}
		return nil, nil
	}
	return statefulSets.Items, nil
}

func (r *Reconciler) getPods(ctx context.Context, req reconcile.Request) ([]corev1.Pod, error) {
	log := ctrl.LoggerFrom(ctx)
	pods := &corev1.PodList{}
	err := r.client.List(ctx, pods, client.InNamespace(req.Namespace),
		client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: req.Name},
	)
	if err != nil {
		log.Error(err, "Failed to get Pods")
		return nil, err
	}
	return pods.Items, nil
}

func (r *Reconciler) reconcileWorkloads(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet) error {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet Workloads")

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(lws.GetNamespace()),
		client.MatchingFields{indexer.OwnerReferenceUID: string(lws.GetUID())},
	); err != nil {
		log.Error(err, "Failed to fetch Workloads")
		return err
	}

	toCreate, toUpdate, toDelete := r.filterWorkloads(lws, wlList.Items)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			return r.createWorkload(ctx, lws, toCreate[i].name, toCreate[i].index)
		})
	})

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toUpdate), func(i int) error {
			return r.updateWorkload(ctx, lws, toUpdate[i])
		})
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
func (r *Reconciler) filterWorkloads(lws *leaderworkersetv1.LeaderWorkerSet, existingWorkloads []kueue.Workload) ([]workloadToCreate, []*kueue.Workload, []*kueue.Workload) {
	var (
		toCreate []workloadToCreate
		toUpdate []*kueue.Workload
		toDelete = utilslices.ToRefMap(existingWorkloads, func(e *kueue.Workload) string {
			return e.Name
		})
		replicas = ptr.Deref(lws.Spec.Replicas, 1)
	)

	// During normal scale-down, status.Replicas lags behind spec.Replicas,
	// which prevents excess workloads from being moved to toDelete on time.
	if lws.Status.Replicas > replicas && isRollingUpdateWithSurge(lws) {
		replicas = lws.Status.Replicas
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

	return toCreate, toUpdate, slices.Collect(maps.Values(toDelete))
}

func isRollingUpdateWithSurge(lws *leaderworkersetv1.LeaderWorkerSet) bool {
	if lws.Spec.RolloutStrategy.RollingUpdateConfiguration == nil {
		return false
	}
	maxSurge := int32(lws.Spec.RolloutStrategy.RollingUpdateConfiguration.MaxSurge.IntValue())
	return maxSurge > 0 && lws.Status.UpdatedReplicas < ptr.Deref(lws.Spec.Replicas, 1)
}

func (r *Reconciler) createWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int) error {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"workload", klog.ObjectRef{Name: workloadName, Namespace: lws.Namespace},
		"index", index,
	)
	log.V(3).Info("Create LeaderWorkerSet Workload")

	createdWorkload, err := r.constructWorkload(lws, workloadName, index)
	if err != nil {
		log.Error(err, "Failed to construct Workload")
		return err
	}

	err = jobframework.PrepareWorkloadPriority(ctx, r.client, lws, createdWorkload, nil)
	if err != nil {
		log.Error(err, "Failed to prepare Workload priority")
		return err
	}

	err = r.client.Create(ctx, createdWorkload)
	if err != nil {
		log.Error(err, "Failed to create Workload")
		return err
	}
	r.record.Eventf(
		lws, nil, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload,
		"CreatedWorkload",
		"Created Workload: %v", workload.Key(createdWorkload),
	)

	jobframework.RecordWorkloadCreationLatency(ctx, lws, lws.GroupVersionKind().Kind, createdWorkload, r.customLabels, r.roleTracker)

	return nil
}

func (r *Reconciler) constructWorkload(lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int) (*kueue.Workload, error) {
	podSets, err := podSets(lws)
	if err != nil {
		return nil, err
	}
	createdWorkload := podcontroller.NewGroupWorkload(workloadName, lws, podSets, r.labelKeysToCopy)

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
		builder := jobframework.NewPodSetTopologyRequest(template.ObjectMeta.DeepCopy())
		if podIndexLabel != nil {
			builder.PodIndexLabel(ptr.To(leaderworkersetv1.WorkerIndexLabelKey))
		}
		topologyRequest, err := builder.Build()
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
	defaultPodSetCount := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1)

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
		ptr.To(leaderworkersetv1.WorkerIndexLabelKey),
	)
	if err != nil {
		return nil, err
	}

	podSets = append(podSets, *workerPodSet)

	return podSets, nil
}

func (r *Reconciler) updateWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Update LeaderWorkerSet Workload")

	podSets, err := podSets(lws)
	if err != nil {
		log.Error(err, "Failed to get pod sets")
		return err
	}
	if !equality.ComparePodSetSlices(podSets, wl.Spec.PodSets) {
		return r.deleteWorkload(ctx, wl)
	}
	if queueName := jobframework.QueueNameForObject(lws); wl.Spec.QueueName != queueName {
		log.V(2).Info("LeaderWorkerSet changed queue, updating workload")
		wl.Spec.QueueName = queueName
		if err := r.client.Update(ctx, wl); err != nil {
			log.Error(err, "Updating workload queue name")
			return err
		}
	}
	if features.Enabled(features.AdmissionGatedBy) {
		if err := jobframework.UpdateAdmissionGatedBy(ctx, r.client, r.record, lws, wl); err != nil {
			log.Error(err, "Failed to update AdmissionGatedBy")
			return err
		}
	}

	err = jobframework.UpdateWorkloadPriority(ctx, r.client, r.record, lws, wl, nil)
	if err != nil {
		log.Error(err, "Failed to update workload priority")
		return err
	}

	return nil
}

func (r *Reconciler) deleteWorkload(ctx context.Context, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Delete LeaderWorkerSet Workload")

	// Remove the finalizer before deleting to ensure prompt cleanup,
	// consistent with how the job framework reconciler deletes workloads.
	if err := workload.RemoveFinalizer(ctx, r.client, wl); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to remove finalizer")
		return err
	}
	err := r.client.Delete(ctx, wl)
	if err != nil {
		log.Error(err, "Failed to delete workload")
		return err
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
		"group", pod.Labels[podconstants.GroupNameLabel])
	if sts != nil {
		log = log.WithValues(
			"statefulset", klog.KObj(sts),
			"currentRevision", sts.Status.CurrentRevision,
			"updateRevision", sts.Status.UpdateRevision,
		)
	}
	log.V(2).Info("Reconcile LeaderWorkerSet Pod")

	if lws == nil || utilstatefulset.ShouldFinalizePod(sts, pod) {
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			if utilstatefulset.UngateAndFinalizePod(sts, pod, lws == nil) {
				log.V(3).Info("Finalizing LeaderWorkerSet Pod")
				return true, nil
			}
			log.V(3).Info("Skipping finalizing LeaderWorkerSet Pod")
			return false, nil
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to finalize Pod")
			return err
		}
	} else {
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated := r.setDefault(lws, pod)
			if updated {
				log.V(3).Info("Setting default values")
			}
			return updated, nil
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to set default values")
			return err
		}
	}

	return nil
}

func (r *Reconciler) setDefault(lws *leaderworkersetv1.LeaderWorkerSet, pod *corev1.Pod) bool {
	// Pod already has managed-by-kueue label, skipping.
	if _, ok := pod.Labels[constants.ManagedByKueueLabelKey]; ok {
		return false
	}

	// We should wait for GroupIndexLabelKey.
	if _, ok := pod.Labels[leaderworkersetv1.GroupIndexLabelKey]; !ok {
		return false
	}

	wlName := GetWorkloadName(GetOwnerUID(lws), lws.Name, pod.Labels[leaderworkersetv1.GroupIndexLabelKey])

	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	pod.Labels[podconstants.GroupNameLabel] = wlName
	pod.Labels[controllerconstants.PrebuiltWorkloadLabel] = wlName
	pod.Annotations[podconstants.GroupTotalCountAnnotation] = fmt.Sprint(ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1))
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

	log := r.logger().WithValues("leaderworkerset", klog.KObj(lws))
	ctx := ctrl.LoggerInto(context.Background(), log)

	// Handle only leaderworkerset managed by kueue.
	suspend, err := jobframework.WorkloadShouldBeSuspended(ctx, lws, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the LeaderWorkerSet should be managed by Kueue")
	}

	return suspend
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
	log.V(3).Info("Enqueue LeaderWorkerSet Workload")

	for _, ownerRef := range wl.OwnerReferences {
		if ownerRef.APIVersion == gvk.GroupVersion().String() && ownerRef.Kind == gvk.Kind {
			log.V(3).Info("Queueing reconcile for owning LeaderWorkerSet",
				"leaderworkerset", klog.ObjectRef{Namespace: wl.Namespace, Name: ownerRef.Name},
			)
			q.AddAfter(
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: wl.Namespace,
						Name:      ownerRef.Name,
					},
				},
				constants.UpdatesBatchPeriod,
			)
			return
		}
	}
}

// lwsPodHandler watches for Pod create and update events and triggers reconciliation
// of the owning LeaderWorkerSet. This ensures that we will finalize and ungate pods
// and set default values.
type lwsPodHandler struct{}

var _ handler.EventHandler = (*lwsPodHandler)(nil)

func (h *lwsPodHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *lwsPodHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *lwsPodHandler) Delete(_ context.Context, _ event.DeleteEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
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
	log.V(3).Info("Enqueue LeaderWorkerSet Pod")

	// Handle only Pods suspended by LeaderWorkerSet.
	if pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		log.V(3).Info("Pod is not suspended by parent")
		return
	}

	lwsName, ok := pod.Labels[leaderworkersetv1.SetNameLabelKey]
	if !ok {
		log.V(3).Info("Pod doesn't have LeaderWorkerSet name label")
		return
	}

	log.V(3).Info("Queueing reconcile for owning LeaderWorkerSet",
		"leaderworkerset", klog.ObjectRef{Namespace: pod.Namespace, Name: lwsName},
	)

	q.AddAfter(
		reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: pod.Namespace,
				Name:      lwsName,
			},
		},
		constants.UpdatesBatchPeriod,
	)
}

// lwsStsHandler watches for StatefulSet update events and triggers reconciliation
// of the owning LeaderWorkerSet.
// Subscribe to StatefulSet updates and watch .Status.CurrentRevision and .Status.UpdateRevision
// to finalize Pods and remove scheduling gates when a new revision appears.
type lwsStsHandler struct{}

var _ handler.EventHandler = (*lwsStsHandler)(nil)

func (h *lwsStsHandler) Create(_ context.Context, _ event.CreateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *lwsStsHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *lwsStsHandler) Delete(_ context.Context, _ event.DeleteEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *lwsStsHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// enqueue adds a reconcile request for the LeaderWorkerSet owning the given StatefulSet to the provided workqueue.
func (h *lwsStsHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues(
		"statefulset", klog.KObj(sts),
		"currentRevision", sts.Status.CurrentRevision,
		"updateRevision", sts.Status.UpdateRevision,
	)
	log.V(3).Info("Enqueue LeaderWorkerSet StatefulSet")

	// Handle only when .Status.CurrentRevision != .Status.UpdateRevision.
	// This ensures that Pods are finalized and scheduling gates are removed
	// when the revision changes.
	if sts.Status.CurrentRevision == "" || sts.Status.UpdateRevision == "" &&
		sts.Status.CurrentRevision == sts.Status.UpdateRevision {
		return
	}

	// Handle only StatefulSets suspended by LeaderWorkerSet.
	if sts.Spec.Template.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		log.V(3).Info("StatefulSet is not suspended by parent")
		return
	}

	lwsName, ok := sts.Labels[leaderworkersetv1.SetNameLabelKey]
	if !ok {
		log.V(3).Info("StatefulSet doesn't have LeaderWorkerSet name label")
		return
	}

	log.V(3).Info("Queueing reconcile for owning LeaderWorkerSet",
		"leaderworkerset", klog.ObjectRef{Namespace: sts.Namespace, Name: lwsName},
	)

	q.AddAfter(
		reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: sts.Namespace,
				Name:      lwsName,
			},
		},
		constants.UpdatesBatchPeriod,
	)
}
