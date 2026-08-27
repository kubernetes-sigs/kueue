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

package disaggregatedset

import (
	"cmp"
	"context"
	"fmt"
	goslices "slices"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
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
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
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
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utilstatefulset "sigs.k8s.io/kueue/pkg/util/statefulset"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	leaderPodSetSuffix = "leader"
	workerPodSetSuffix = "worker"
	mainPodSetSuffix   = "main"
	dsDomainPrefix     = "disaggregatedset.x-k8s.io"
	lwsDomainPrefix    = "leaderworkerset.sigs.k8s.io"
)

type Reconciler struct {
	integrationManager           *jobframework.IntegrationManager
	client                       client.Client
	logName                      string
	record                       events.EventRecorder
	labelKeysToCopy              sets.Set[string]
	annotationsToCopy            sets.Set[string]
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
	customLabels                 *metrics.CustomLabels
}

const controllerName = "disaggregatedset"

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, eventRecorder events.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		integrationManager:           options.IntegrationManager,
		client:                       client,
		logName:                      "disaggregatedset-reconciler",
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
	ctrl.Log.V(3).Info("Setting up DisaggregatedSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&disaggregatedsetv1.DisaggregatedSet{}, builder.WithPredicates(r)).
		Named(controllerName).
		Watches(&kueue.Workload{}, &dsWorkloadHandler{}).
		Watches(&corev1.Pod{}, &dsPodHandler{}).
		Watches(&appsv1.StatefulSet{}, &dsStsHandler{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, controllerName),
		}).
		Complete(r)
}

// +kubebuilder:rbac:groups=disaggregatedset.x-k8s.io,resources=disaggregatedsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=disaggregatedset.x-k8s.io,resources=disaggregatedsets/status,verbs=get;patch;update

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile DisaggregatedSet")

	ds, err := r.getDisaggregatedSet(ctx, req)
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

	var workloadAdmitted bool
	if ds != nil {
		workloadAdmitted, err = r.reconcileWorkloads(ctx, ds)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	err = r.reconcilePods(ctx, ds, statefulSets, pods, workloadAdmitted)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) getDisaggregatedSet(ctx context.Context, req reconcile.Request) (*disaggregatedsetv1.DisaggregatedSet, error) {
	log := ctrl.LoggerFrom(ctx)
	ds := &disaggregatedsetv1.DisaggregatedSet{}
	if err := r.client.Get(ctx, req.NamespacedName, ds); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get DisaggregatedSet")
			return nil, err
		}
		return nil, nil
	}
	return ds, nil
}

func (r *Reconciler) getStatefulSets(ctx context.Context, req reconcile.Request) ([]appsv1.StatefulSet, error) {
	log := ctrl.LoggerFrom(ctx)
	statefulSets := &appsv1.StatefulSetList{}
	if err := r.client.List(ctx, statefulSets, client.InNamespace(req.Namespace),
		client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: req.Name},
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
		client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: req.Name},
	)
	if err != nil {
		log.Error(err, "Failed to get Pods")
		return nil, err
	}
	return pods.Items, nil
}

func (r *Reconciler) reconcileWorkloads(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile DisaggregatedSet Workloads")

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(ds.GetNamespace()),
		client.MatchingFields{indexer.OwnerReferenceUID: string(ds.GetUID())},
	); err != nil {
		log.Error(err, "Failed to fetch Workloads")
		return false, err
	}

	expectedName := GetWorkloadName(ds.UID, ds.Name)
	var toUpdate *kueue.Workload
	var toDelete []*kueue.Workload
	found := false

	for i := range wlList.Items {
		wl := &wlList.Items[i]
		if wl.Name == expectedName {
			toUpdate = wl
			found = true
		} else {
			toDelete = append(toDelete, wl)
		}
	}

	for _, wl := range toDelete {
		if err := r.deleteWorkload(ctx, wl); err != nil {
			return false, err
		}
	}

	if found {
		return workload.IsAdmitted(toUpdate), r.updateWorkload(ctx, ds, toUpdate)
	}
	return false, r.createWorkload(ctx, ds, expectedName)
}

func (r *Reconciler) createWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, workloadName string) error {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"workload", klog.ObjectRef{Name: workloadName, Namespace: ds.Namespace},
	)
	log.V(3).Info("Create DisaggregatedSet Workload")

	wl, err := r.constructWorkload(ds, workloadName)
	if err != nil {
		log.Error(err, "Failed to construct Workload")
		return err
	}

	err = jobframework.PrepareWorkloadPriority(ctx, r.client, r.record, ds, wl, nil)
	if err != nil {
		log.Error(err, "Failed to prepare Workload priority")
		return err
	}

	err = r.client.Create(ctx, wl)
	if err != nil {
		log.Error(err, "Failed to create Workload")
		return err
	}
	r.record.Eventf(
		ds, nil, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload,
		"CreatedWorkload",
		"Created Workload: %v", workload.Key(wl),
	)

	jobframework.RecordWorkloadCreationLatency(ctx, ds, ds.GroupVersionKind().Kind, wl, r.customLabels, r.roleTracker)

	return nil
}

func (r *Reconciler) constructWorkload(ds *disaggregatedsetv1.DisaggregatedSet, workloadName string) (*kueue.Workload, error) {
	ps, err := podSets(ds)
	if err != nil {
		return nil, err
	}
	wl := jobframework.NewWorkload(workloadName, ds, ps, r.labelKeysToCopy, r.annotationsToCopy)
	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string)
	}
	wl.Annotations[podconstants.IsGroupWorkloadAnnotationKey] = podconstants.IsGroupWorkloadAnnotationValue

	if wl.Labels == nil {
		wl.Labels = make(map[string]string, 1)
	}
	wl.Labels[controllerconstants.JobUIDLabel] = string(ds.UID)

	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string)
	}
	wl.Annotations[controllerconstants.JobOwnerGVKAnnotation] = gvk.String()
	wl.Annotations[controllerconstants.JobOwnerNameAnnotation] = ds.Name

	if features.Enabled(features.AdmissionGatedBy) {
		jobframework.PropagateAdmissionGatedByAnnotation(ds, wl)
	}

	if err := controllerutil.SetOwnerReference(ds, wl, r.client.Scheme()); err != nil {
		return nil, err
	}
	return wl, nil
}

func dropDSPrefixedKeys(m map[string]string) {
	for k := range m {
		if strings.HasPrefix(k, dsDomainPrefix) && k != disaggregatedsetv1.SetNameLabelKey {
			delete(m, k)
		}
	}
}

func dropLWSPrefixedKeys(m map[string]string) {
	for k := range m {
		if strings.HasPrefix(k, lwsDomainPrefix) {
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
	dropDSPrefixedKeys(podSet.Template.Labels)
	dropDSPrefixedKeys(podSet.Template.Annotations)
	dropLWSPrefixedKeys(podSet.Template.Labels)
	dropLWSPrefixedKeys(podSet.Template.Annotations)
	jobframework.SanitizePodSet(podSet)
	if features.Enabled(features.TopologyAwareScheduling) {
		b := jobframework.NewPodSetTopologyRequest(template.ObjectMeta.DeepCopy())
		if podIndexLabel != nil {
			b.PodIndexLabel(new(leaderworkersetv1.WorkerIndexLabelKey))
		}
		topologyRequest, err := b.Build()
		if err != nil {
			return nil, err
		}
		podSet.TopologyRequest = topologyRequest
	}
	return podSet, nil
}

func podSets(ds *disaggregatedsetv1.DisaggregatedSet) ([]kueue.PodSet, error) {
	slices := ptr.Deref(ds.Spec.Slices, int32(defaultSlices))
	result := make([]kueue.PodSet, 0, len(ds.Spec.Roles)*2)

	for _, role := range ds.Spec.Roles {
		replicas := ptr.Deref(role.Spec.Replicas, int32(defaultReplicas))
		size := ptr.Deref(role.Spec.LeaderWorkerTemplate.Size, int32(defaultSize))

		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			leaderCount := slices * replicas
			leaderPS, err := newPodSet(
				kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, leaderPodSetSuffix)),
				leaderCount,
				role.Spec.LeaderWorkerTemplate.LeaderTemplate,
				nil,
			)
			if err != nil {
				return nil, err
			}
			result = append(result, *leaderPS)

			workerCount := slices * replicas * (size - 1)
			if workerCount > 0 {
				workerPS, err := newPodSet(
					kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, workerPodSetSuffix)),
					workerCount,
					&role.Spec.LeaderWorkerTemplate.WorkerTemplate,
					new(leaderworkersetv1.WorkerIndexLabelKey),
				)
				if err != nil {
					return nil, err
				}
				result = append(result, *workerPS)
			}
		} else {
			mainCount := slices * replicas * size
			mainPS, err := newPodSet(
				kueue.PodSetReference(fmt.Sprintf("%s-%s", role.Name, mainPodSetSuffix)),
				mainCount,
				&role.Spec.LeaderWorkerTemplate.WorkerTemplate,
				new(leaderworkersetv1.WorkerIndexLabelKey),
			)
			if err != nil {
				return nil, err
			}
			result = append(result, *mainPS)
		}
	}

	goslices.SortFunc(result, func(a, b kueue.PodSet) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return result, nil
}

func (r *Reconciler) updateWorkload(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Update DisaggregatedSet Workload")

	var shouldUpdate bool
	if queueName := jobframework.QueueNameForObject(ds); wl.Spec.QueueName != queueName {
		log.V(2).Info("DisaggregatedSet changed queue, updating workload")
		wl.Spec.QueueName = queueName
		shouldUpdate = true
	}

	var admissionGatedByUpdated bool
	if features.Enabled(features.AdmissionGatedBy) {
		admissionGatedByUpdated = jobframework.PropagateAdmissionGatedByAnnotation(ds, wl)
		shouldUpdate = admissionGatedByUpdated || shouldUpdate
	}

	if shouldUpdate {
		if err := r.client.Update(ctx, wl); err != nil {
			log.Error(err, "Updating workload")
			return err
		}
	}
	if admissionGatedByUpdated {
		jobframework.RecordAdmissionGatedByUpdateEvent(r.record, ds)
	}

	err := jobframework.UpdateWorkloadPriority(ctx, r.client, r.record, ds, nil, wl)
	if err != nil {
		log.Error(err, "Failed to update workload priority")
		return err
	}

	return nil
}

func (r *Reconciler) deleteWorkload(ctx context.Context, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Delete DisaggregatedSet Workload")

	_, err := workload.Delete(ctx, r.client, wl)
	if err != nil {
		log.Error(err, "Failed to delete workload")
	}
	return err
}

func (r *Reconciler) reconcilePods(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, statefulSets []appsv1.StatefulSet, pods []corev1.Pod, workloadAdmitted bool) error {
	statefulSetsMap := utilslices.ToRefMap(statefulSets, func(e *appsv1.StatefulSet) string {
		return e.Name
	})

	return parallelize.Until(ctx, len(pods), func(i int) error {
		pod := &pods[i]
		var sts *appsv1.StatefulSet
		if ref := metav1.GetControllerOf(pod); ref != nil {
			sts = statefulSetsMap[ref.Name]
		}
		return r.reconcilePod(ctx, ds, sts, pod, workloadAdmitted)
	})
}

func (r *Reconciler) reconcilePod(ctx context.Context, ds *disaggregatedsetv1.DisaggregatedSet, sts *appsv1.StatefulSet, pod *corev1.Pod, workloadAdmitted bool) error {
	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))
	log.V(2).Info("Reconcile DisaggregatedSet Pod")

	shouldUngate := ds == nil ||
		(sts != nil && utilstatefulset.ShouldUngatePod(sts, pod)) ||
		workloadAdmitted

	if shouldUngate {
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			if utilstatefulset.UngatePod(sts, pod, ds == nil) {
				log.V(3).Info("Ungating DisaggregatedSet Pod")
				return true, nil
			}
			return false, nil
		})
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to ungate Pod")
			return err
		}
	}

	if ds != nil && !utilpod.IsTerminated(pod) && pod.DeletionTimestamp == nil {
		err := clientutil.Patch(ctx, r.client, pod, func() (bool, error) {
			updated := r.setDefault(ds, pod)
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

func (r *Reconciler) setDefault(ds *disaggregatedsetv1.DisaggregatedSet, pod *corev1.Pod) bool {
	// Wait for the role label to be set by the DS controller.
	// With LeaderReady startup policy, pods appear gradually; the label
	// may not be present yet on newly created pods.
	roleName, ok := pod.Labels[disaggregatedsetv1.RoleLabelKey]
	if !ok {
		return false
	}

	if _, ok := pod.Labels[constants.ManagedByKueueLabelKey]; ok {
		return false
	}

	wlName := GetWorkloadName(ds.UID, ds.Name)

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
	podcontroller.SetPodGroupName(pod, wlName)
	jobframework.SetPrebuiltWorkloadName(pod, wlName)

	totalCount := totalPodCount(ds)
	pod.Annotations[podconstants.GroupTotalCountAnnotation] = fmt.Sprint(totalCount)

	role := findRole(ds, roleName)
	pod.Annotations[podconstants.RoleHashAnnotation] = podSetNameForPod(roleName, role, pod)

	return true
}

func totalPodCount(ds *disaggregatedsetv1.DisaggregatedSet) int32 {
	slices := ptr.Deref(ds.Spec.Slices, int32(defaultSlices))
	var total int32
	for _, role := range ds.Spec.Roles {
		replicas := ptr.Deref(role.Spec.Replicas, int32(defaultReplicas))
		size := ptr.Deref(role.Spec.LeaderWorkerTemplate.Size, int32(defaultSize))
		total += slices * replicas * size
	}
	return total
}

func findRole(ds *disaggregatedsetv1.DisaggregatedSet, roleName string) *disaggregatedsetv1.DisaggregatedRoleSpec {
	for i := range ds.Spec.Roles {
		if ds.Spec.Roles[i].Name == roleName {
			return &ds.Spec.Roles[i]
		}
	}
	return nil
}

func podSetNameForPod(roleName string, role *disaggregatedsetv1.DisaggregatedRoleSpec, pod *corev1.Pod) string {
	if role == nil || role.Spec.LeaderWorkerTemplate.LeaderTemplate == nil {
		return fmt.Sprintf("%s-%s", roleName, mainPodSetSuffix)
	}
	// LeaderPodNameAnnotationKey is set on worker pods to identify their leader.
	// Its absence means this pod is a leader.
	if _, hasLeaderAnnotation := pod.Annotations[leaderworkersetv1.LeaderPodNameAnnotationKey]; hasLeaderAnnotation {
		return fmt.Sprintf("%s-%s", roleName, workerPodSetSuffix)
	}
	return fmt.Sprintf("%s-%s", roleName, leaderPodSetSuffix)
}

// Predicate filtering

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
	ds, ok := obj.(*disaggregatedsetv1.DisaggregatedSet)
	if !ok {
		return false
	}

	log := r.logger().WithValues("disaggregatedset", klog.KObj(ds))
	ctx := ctrl.LoggerInto(context.Background(), log)

	suspend, err := r.integrationManager.WorkloadShouldBeSuspended(ctx, ds, r.client, r.manageJobsWithoutQueueName, r.managedJobsNamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to determine if the DisaggregatedSet should be managed by Kueue")
	}

	return suspend
}

// dsWorkloadHandler watches for workload events and triggers reconciliation
// of the owning DisaggregatedSet.
type dsWorkloadHandler struct{}

var _ handler.EventHandler = (*dsWorkloadHandler)(nil)

func (h *dsWorkloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *dsWorkloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *dsWorkloadHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsWorkloadHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *dsWorkloadHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	wl, ok := obj.(*kueue.Workload)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(wl))
	log.V(3).Info("Enqueue DisaggregatedSet Workload")

	for _, ownerRef := range wl.OwnerReferences {
		if ownerRef.APIVersion == gvk.GroupVersion().String() && ownerRef.Kind == gvk.Kind {
			log.V(3).Info("Queueing reconcile for owning DisaggregatedSet",
				"disaggregatedset", klog.ObjectRef{Namespace: wl.Namespace, Name: ownerRef.Name},
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

// dsPodHandler watches for Pod create and update events and triggers reconciliation
// of the owning DisaggregatedSet.
type dsPodHandler struct{}

var _ handler.EventHandler = (*dsPodHandler)(nil)

func (h *dsPodHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.Object, q)
}

func (h *dsPodHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *dsPodHandler) Delete(_ context.Context, _ event.DeleteEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsPodHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsPodHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(pod))
	log.V(3).Info("Enqueue DisaggregatedSet Pod")

	if pod.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		return
	}

	dsName, ok := pod.Labels[disaggregatedsetv1.SetNameLabelKey]
	if !ok {
		return
	}

	log.V(3).Info("Queueing reconcile for owning DisaggregatedSet",
		"disaggregatedset", klog.ObjectRef{Namespace: pod.Namespace, Name: dsName},
	)

	q.AddAfter(
		reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: pod.Namespace,
				Name:      dsName,
			},
		},
		constants.UpdatesBatchPeriod,
	)
}

// dsStsHandler watches for StatefulSet update events and triggers reconciliation
// of the owning DisaggregatedSet. This handles revision changes during rolling
// updates, ensuring old-revision pods get ungated.
type dsStsHandler struct{}

var _ handler.EventHandler = (*dsStsHandler)(nil)

func (h *dsStsHandler) Create(_ context.Context, _ event.CreateEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsStsHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.enqueue(ctx, e.ObjectNew, q)
}

func (h *dsStsHandler) Delete(_ context.Context, _ event.DeleteEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsStsHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (h *dsStsHandler) enqueue(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	sts, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues(
		"statefulset", klog.KObj(sts),
		"currentRevision", sts.Status.CurrentRevision,
		"updateRevision", sts.Status.UpdateRevision,
	)
	log.V(3).Info("Enqueue DisaggregatedSet StatefulSet")

	if sts.Status.CurrentRevision == "" || sts.Status.UpdateRevision == "" &&
		sts.Status.CurrentRevision == sts.Status.UpdateRevision {
		return
	}

	if sts.Spec.Template.Annotations[podconstants.SuspendedByParentAnnotation] != FrameworkName {
		return
	}

	dsName, ok := sts.Labels[disaggregatedsetv1.SetNameLabelKey]
	if !ok {
		return
	}

	log.V(3).Info("Queueing reconcile for owning DisaggregatedSet",
		"disaggregatedset", klog.ObjectRef{Namespace: sts.Namespace, Name: dsName},
	)

	q.AddAfter(
		reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: sts.Namespace,
				Name:      dsName,
			},
		},
		constants.UpdatesBatchPeriod,
	)
}
