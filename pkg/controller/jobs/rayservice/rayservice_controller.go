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
	"k8s.io/apimachinery/pkg/api/meta"
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
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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
		SetupIndexes:  SetupIndexes,
		NewJob:        newJob,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupRayServiceWebhook,
		JobType:       &rayv1.RayService{},
		AddToScheme:   rayv1.AddToScheme,
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
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(rayv1.RayServiceReady))
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

func (j *RayService) PodSets(ctx context.Context) ([]kueue.PodSet, error) {
	// Always build PodSets from RayService spec first
	podSets, err := raycluster.BuildPodSets(&j.Spec.RayClusterSpec)
	if err != nil {
		return nil, err
	}

	rayClusterName := j.Status.ActiveServiceStatus.RayClusterName
	podSets, err = raycluster.UpdatePodSets(ctx, podSets, reconciler.client, j.Object(), j.Spec.RayClusterSpec.EnableInTreeAutoscaling, rayClusterName)
	if err != nil {
		return nil, err
	}

	return podSets, nil
}

func (j *RayService) RunWithPodSetsInfo(ctx context.Context, podSetsInfo []podset.PodSetInfo) error {
	expectedLen := len(j.Spec.RayClusterSpec.WorkerGroupSpecs) + 1
	if len(podSetsInfo) != expectedLen {
		return podset.BadPodSetsInfoLenError(expectedLen, len(podSetsInfo))
	}

	j.Spec.RayClusterSpec.Suspend = ptr.To(false)

	rayClusterSpec := &j.Spec.RayClusterSpec
	err := raycluster.UpdateRayClusterSpecToRunWithPodSetsInfo(rayClusterSpec, podSetsInfo)
	if err != nil {
		return err
	}

	return nil
}

func (j *RayService) RestorePodSetsInfo(podSetsInfo []podset.PodSetInfo) bool {
	if len(podSetsInfo) != len(j.Spec.RayClusterSpec.WorkerGroupSpecs)+1 {
		return false
	}

	return raycluster.RestorePodSetsInfo(&j.Spec.RayClusterSpec, podSetsInfo)
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
	return meta.IsStatusConditionTrue(j.Status.Conditions, string(rayv1.RayServiceReady))
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func GetWorkloadNameForRayService(serviceName string, serviceUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(serviceName, serviceUID, gvk)
}
