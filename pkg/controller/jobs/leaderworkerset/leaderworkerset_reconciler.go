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

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	leaderPodSetName = "leader"
	workerPodSetName = "worker"
)

type workloadToCreate struct {
	name  string
	index int
}

type Reconciler struct {
	client                       client.Client
	logName                      string
	record                       record.EventRecorder
	labelKeysToCopy              []string
	manageJobsWithoutQueueName   bool
	managedJobsNamespaceSelector labels.Selector
	roleTracker                  *roletracker.RoleTracker
}

const controllerName = "leaderworkerset"

func NewReconciler(_ context.Context, client client.Client, _ client.FieldIndexer, eventRecorder record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	options := jobframework.ProcessOptions(opts...)

	return &Reconciler{
		client:                       client,
		logName:                      "leaderworkerset-reconciler",
		record:                       eventRecorder,
		labelKeysToCopy:              options.LabelKeysToCopy,
		manageJobsWithoutQueueName:   options.ManageJobsWithoutQueueName,
		managedJobsNamespaceSelector: options.ManagedJobsNamespaceSelector,
		roleTracker:                  options.RoleTracker,
	}, nil
}

func (r *Reconciler) logger() logr.Logger {
	return roletracker.WithReplicaRole(ctrl.Log.WithName(r.logName), r.roleTracker)
}

var _ jobframework.JobReconcilerInterface = (*Reconciler)(nil)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrl.Log.V(3).Info("Setting up LeaderWorkerSet reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&leaderworkersetv1.LeaderWorkerSet{}).
		Named(controllerName).
		WithEventFilter(r).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.roleTracker, controllerName),
		}).
		Complete(r)
}

// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets,verbs=get;list;watch
// +kubebuilder:rbac:groups=leaderworkerset.x-k8s.io,resources=leaderworkersets/status,verbs=get;patch;update

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	lws := &leaderworkersetv1.LeaderWorkerSet{}
	err := r.client.Get(ctx, req.NamespacedName, lws)
	if err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile LeaderWorkerSet")

	wlList := &kueue.WorkloadList{}
	if err := r.client.List(ctx, wlList, client.InNamespace(lws.GetNamespace()),
		client.MatchingFields{indexer.OwnerReferenceUID: string(lws.GetUID())},
	); err != nil {
		return ctrl.Result{}, err
	}

	toCreate, toUpdate, toDelete := r.filterWorkloads(lws, wlList.Items)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toCreate), func(i int) error {
			return r.createPrebuiltWorkload(ctx, lws, toCreate[i].name, toCreate[i].index)
		})
	})

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toUpdate), func(i int) error {
			return jobframework.UpdateWorkloadPriority(ctx, r.client, r.record, lws, toUpdate[i], nil)
		})
	})

	eg.Go(func() error {
		return parallelize.Until(ctx, len(toDelete), func(i int) error {
			// Remove the finalizer before deleting to ensure prompt cleanup,
			// consistent with how the job framework reconciler deletes workloads.
			if err := workload.RemoveFinalizer(ctx, r.client, toDelete[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return r.client.Delete(ctx, toDelete[i])
		})
	})

	err = eg.Wait()
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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

func (r *Reconciler) createPrebuiltWorkload(ctx context.Context, lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int) error {
	createdWorkload, err := r.constructWorkload(lws, workloadName, index)
	if err != nil {
		return err
	}

	err = jobframework.PrepareWorkloadPriority(ctx, r.client, lws, createdWorkload, nil)
	if err != nil {
		return err
	}

	err = r.client.Create(ctx, createdWorkload)
	if err != nil {
		return err
	}
	r.record.Eventf(
		lws, corev1.EventTypeNormal, jobframework.ReasonCreatedWorkload,
		"Created Workload: %v", workload.Key(createdWorkload),
	)
	return nil
}

func (r *Reconciler) constructWorkload(lws *leaderworkersetv1.LeaderWorkerSet, workloadName string, index int) (*kueue.Workload, error) {
	podSets, err := podSets(lws)
	if err != nil {
		return nil, err
	}
	createdWorkload := podcontroller.NewGroupWorkload(workloadName, lws, podSets, r.labelKeysToCopy)

	// Add job owner annotations for reliable MultiKueue adapter lookup.
	// These annotations persist even after Kubernetes GC removes owner references.
	if createdWorkload.Annotations == nil {
		createdWorkload.Annotations = make(map[string]string)
	}
	createdWorkload.Annotations[constants.JobOwnerGVKAnnotation] = gvk.String()
	createdWorkload.Annotations[constants.JobOwnerNameAnnotation] = lws.Name
	createdWorkload.Annotations[constants.ComponentWorkloadIndexAnnotation] = strconv.Itoa(index)

	if err := controllerutil.SetOwnerReference(lws, createdWorkload, r.client.Scheme()); err != nil {
		return nil, err
	}
	return createdWorkload, nil
}

func newPodSet(name kueue.PodSetReference, count int32, template *corev1.PodTemplateSpec, podIndexLabel *string) (*kueue.PodSet, error) {
	podSet := &kueue.PodSet{
		Name:  name,
		Count: count,
		Template: corev1.PodTemplateSpec{
			Spec: *template.Spec.DeepCopy(),
		},
	}
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
