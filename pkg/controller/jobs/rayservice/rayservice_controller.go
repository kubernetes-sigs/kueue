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

package rayservice

import (
	"context"
	"fmt"
	"strings"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	gvk = rayv1.GroupVersion.WithKind("RayService")
)

const (
	headGroupPodSetName = "head"
	FrameworkName       = "ray.io/rayservice"
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            newJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupRayServiceWebhook,
		JobType:           &rayv1.RayService{},
		AddToScheme:       rayv1.AddToScheme,
		MultiKueueAdapter: &multiKueueAdapter{},
	}))
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups=ray.io,resources=rayservices,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloadpriorityclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=ray.io,resources=rayservices/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=ray.io,resources=rayclusters,verbs=get;list;watch

type rayServiceReconciler struct {
	jr     *jobframework.JobReconciler
	client client.Client
}

func newJob() jobframework.GenericJob {
	return &RayService{}
}

func setup(b *builder.Builder, c client.Client) *builder.Builder {
	return b.Watches(&rayv1.RayCluster{}, handler.EnqueueRequestForOwner(c.Scheme(), c.RESTMapper(), &rayv1.RayService{}, handler.OnlyControllerOwner()))
}

var reconciler rayServiceReconciler

func NewReconciler(ctx context.Context, client client.Client, indexer client.FieldIndexer, eventRecorder record.EventRecorder, opts ...jobframework.Option) (jobframework.JobReconcilerInterface, error) {
	reconciler = rayServiceReconciler{
		jr:     jobframework.NewReconciler(client, eventRecorder, opts...),
		client: client,
	}
	return &reconciler, nil
}

func (r *rayServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.jr.ReconcileGenericJob(ctx, req, newJob())
}

func (r *rayServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controllerName := strings.ToLower(newJob().GVK().Kind)
	b := ctrl.NewControllerManagedBy(mgr).
		For(newJob().Object()).Owns(&kueue.Workload{}).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(r.jr.RoleTracker(), controllerName),
		})
	c := mgr.GetClient()
	b = setup(b, c)
	return b.Complete(r)
}

type RayService rayv1.RayService

var _ jobframework.GenericJob = (*RayService)(nil)

func (j *RayService) Object() client.Object {
	return (*rayv1.RayService)(j)
}

func (j *RayService) IsSuspended() bool {
	return j.Spec.RayClusterSpec.Suspend != nil && *j.Spec.RayClusterSpec.Suspend
}

func (j *RayService) IsActive() bool {
	//nolint:staticcheck //SA1019: Status.ServiceStatus is deprecated but still functional
	return j.Status.ServiceStatus == rayv1.Running
}

func (j *RayService) Suspend() {
	j.Spec.RayClusterSpec.Suspend = ptr.To(true)
}

func (j *RayService) GVK() schema.GroupVersionKind {
	return gvk
}

func (j *RayService) PodLabelSelector() string {
	if j.Status.ActiveServiceStatus.RayClusterName != "" {
		return fmt.Sprintf("%s=%s", rayutils.RayClusterLabelKey, j.Status.ActiveServiceStatus.RayClusterName)
	}
	return ""
}

// buildPodSetsFromRayServiceSpec builds PodSets from RayService's RayClusterSpec
func (j *RayService) buildPodSetsFromRayServiceSpec() ([]kueue.PodSet, error) {
	podSets := make([]kueue.PodSet, 0)

	// head
	headPodSet := kueue.PodSet{
		Name:     headGroupPodSetName,
		Template: *j.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
		Count:    1,
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&j.Spec.RayClusterSpec.HeadGroupSpec.Template.ObjectMeta).Build()
		if err != nil {
			return nil, err
		}
		headPodSet.TopologyRequest = topologyRequest
	}
	podSets = append(podSets, headPodSet)

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index]
		count := int32(1)
		if wgs.Replicas != nil {
			count = *wgs.Replicas
		}
		if wgs.NumOfHosts > 1 {
			count *= wgs.NumOfHosts
		}
		workerPodSet := kueue.PodSet{
			Name:     kueue.NewPodSetReference(wgs.GroupName),
			Template: *wgs.Template.DeepCopy(),
			Count:    count,
		}
		if features.Enabled(features.TopologyAwareScheduling) {
			topologyRequest, err := jobframework.NewPodSetTopologyRequest(&wgs.Template.ObjectMeta).Build()
			if err != nil {
				return nil, err
			}
			workerPodSet.TopologyRequest = topologyRequest
		}
		podSets = append(podSets, workerPodSet)
	}

	return podSets, nil
}

func (j *RayService) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	log := ctrl.LoggerFrom(ctx)

	// Always build PodSets from RayService spec first
	podSets, err := j.buildPodSetsFromRayServiceSpec()
	if err != nil {
		return nil, err
	}

	// Only update podSets from RayCluster if:
	// 1. The service is workload slicing enabled
	// 2. AND the service has enableInTreeAutoscaling
	if workloadslicing.Enabled(j.Object()) && ptr.Deref(j.Spec.RayClusterSpec.EnableInTreeAutoscaling, false) {
		// If ActiveServiceStatus.RayClusterName is set in status, try to fetch the RayCluster and update PodSets from it
		if j.Status.ActiveServiceStatus.RayClusterName != "" {
			var rayClusterObj rayv1.RayCluster
			err := reconciler.client.Get(ctx, types.NamespacedName{
				Namespace: j.Namespace,
				Name:      j.Status.ActiveServiceStatus.RayClusterName,
			}, &rayClusterObj)
			if err != nil {
				// Check if the error is a NotFound error
				if apierrors.IsNotFound(err) {
					log.V(2).Info("RayCluster does not exist, falling back to RayService spec",
						"rayCluster", j.Status.ActiveServiceStatus.RayClusterName)
				} else {
					return nil, fmt.Errorf("failed to get RayCluster %s: %w", j.Status.ActiveServiceStatus.RayClusterName, err)
				}
			} else {
				// Create a map of podSets from RayService spec for quick lookup by name
				podSetMap := make(map[kueue.PodSetReference]*kueue.PodSet)
				for i := range podSets {
					podSetMap[podSets[i].Name] = &podSets[i]
				}

				// Iterate through RayCluster's worker groups and update the count in matching podSets
				for i := range rayClusterObj.Spec.WorkerGroupSpecs {
					wgs := &rayClusterObj.Spec.WorkerGroupSpecs[i]
					podSetName := kueue.NewPodSetReference(wgs.GroupName)

					podSet, exists := podSetMap[podSetName]
					if !exists {
						return nil, fmt.Errorf("PodSet name mismatch: RayCluster %s has worker group %s which is not found in RayService %s spec", j.Status.ActiveServiceStatus.RayClusterName, wgs.GroupName, j.Name)
					}

					if wgs.Replicas == nil {
						continue
					}

					// Calculate the count based on RayCluster's worker group replicas
					count := *wgs.Replicas
					if wgs.NumOfHosts > 1 {
						count *= wgs.NumOfHosts
					}

					// Update the count in the PodSet only if it's different
					if podSet.Count != count {
						log.V(2).Info("Updated RayService PodSet worker count from RayCluster",
							"rayService", j.Name,
							"rayCluster", j.Status.ActiveServiceStatus.RayClusterName,
							"workerGroup", wgs.GroupName,
							"oldCount", podSet.Count,
							"newCount", count)
						podSet.Count = count
					}
				}
			}
		}
	}

	return podSets, nil
}

func (j *RayService) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.RayClusterSpec.Suspend = ptr.To(false)

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	info := podSetsInfo[0]
	if err := podset.Merge(&headPod.ObjectMeta, &headPod.Spec, info); err != nil {
		return err
	}

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template

		info := podSetsInfo[index+1]
		if err := podset.Merge(&workerPod.ObjectMeta, &workerPod.Spec, info); err != nil {
			return err
		}
	}
	return nil
}

func (j *RayService) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	// head
	headPod := &j.Spec.RayClusterSpec.HeadGroupSpec.Template
	changed := podset.RestorePodSpec(&headPod.ObjectMeta, &headPod.Spec, podSetsInfo[0])

	// workers
	for index := range j.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerPod := &j.Spec.RayClusterSpec.WorkerGroupSpecs[index].Template
		info := podSetsInfo[index+1]
		changed = podset.RestorePodSpec(&workerPod.ObjectMeta, &workerPod.Spec, info) || changed
	}

	return changed
}

func (j *RayService) Finished(ctx context.Context) (message string, success, finished bool) {
	// RayService is a long-running service, not a batch job, use hard-coded "Running" message
	message = "Running"
	success = false
	// RayService doesn't have terminal failure states like jobs do
	// It's meant to run continuously
	finished = false
	return message, success, finished
}

func (j *RayService) PodsReady(ctx context.Context) bool {
	//nolint:staticcheck //SA1019: Status.ServiceStatus is deprecated but still functional
	return j.Status.ServiceStatus == rayv1.Running
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayService(serviceName string, serviceUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(serviceName, serviceUID, gvk)
}
