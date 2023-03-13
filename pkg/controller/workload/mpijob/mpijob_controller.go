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

package mpijob

import (
	"context"
	"strings"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/jobframework"
)

var (
	gvk = metav1.GroupVersionKind{Group: kubeflow.SchemeGroupVersion.Group, Version: kubeflow.SchemeGroupVersion.Version, Kind: kubeflow.SchemeGroupVersionKind.Kind}
)

// MPIJobReconciler reconciles a Job object
type MPIJobReconciler struct {
	client               client.Client
	genericJobReconciler *jobframework.GenericJobReconciler
}

type options struct {
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
}

// Option configures the reconciler.
type Option func(*options)

// WithManageJobsWithoutQueueName indicates if the controller should reconcile
// jobs that don't set the queue name annotation.
func WithManageJobsWithoutQueueName(f bool) Option {
	return func(o *options) {
		o.manageJobsWithoutQueueName = f
	}
}

// WithWaitForPodsReady indicates if the controller should add the PodsReady
// condition to the workload when the corresponding job has all pods ready
// or succeeded.
func WithWaitForPodsReady(f bool) Option {
	return func(o *options) {
		o.waitForPodsReady = f
	}
}

var defaultOptions = options{}

func NewReconciler(
	scheme *runtime.Scheme,
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *MPIJobReconciler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	genericJobReconciler := jobframework.NewReconciler(scheme,
		client,
		record,
		jobframework.WithWaitForPodsReady(options.waitForPodsReady),
		jobframework.WithManageJobsWithoutQueueName(options.manageJobsWithoutQueueName),
	)

	return &MPIJobReconciler{
		client:               client,
		genericJobReconciler: genericJobReconciler,
	}
}

type MPIJob struct {
	kubeflow.MPIJob
}

func (job *MPIJob) Object() client.Object {
	return &job.MPIJob
}

func (job *MPIJob) ParentWorkloadName() string {
	return job.Annotations[constants.ParentWorkloadAnnotation]
}

func (job *MPIJob) QueueName() string {
	return job.Annotations[constants.QueueAnnotation]
}

func (job *MPIJob) IsSuspend() bool {
	return job.Spec.RunPolicy.Suspend != nil && *job.Spec.RunPolicy.Suspend
}

func (job *MPIJob) IsActive() bool {
	for _, replicaStatus := range job.Status.ReplicaStatuses {
		if replicaStatus.Active != 0 {
			return true
		}
	}
	return false
}

func (job *MPIJob) Suspend() error {
	job.Spec.RunPolicy.Suspend = pointer.Bool(true)
	return nil
}

func (job *MPIJob) UnSuspend() error {
	job.Spec.RunPolicy.Suspend = pointer.Bool(false)
	return nil
}

func (job *MPIJob) ResetStatus() bool {
	if job.Status.StartTime == nil {
		return false
	}
	job.Status.StartTime = nil
	return true
}

func (job *MPIJob) GetGVK() *metav1.GroupVersionKind {
	return &gvk
}

func (job *MPIJob) PodSets() []kueue.PodSet {
	replicaTypes := orderedReplicaTypes(&job.Spec)
	podSets := make([]kueue.PodSet, len(replicaTypes))
	for index, mpiReplicaType := range replicaTypes {
		podSets[index] = kueue.PodSet{
			Name:     strings.ToLower(string(mpiReplicaType)),
			Template: *job.Spec.MPIReplicaSpecs[mpiReplicaType].Template.DeepCopy(),
			Count:    podsCount(&job.Spec, mpiReplicaType),
		}
	}
	return podSets
}

func (job *MPIJob) InjectNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 {
		return nil
	}
	orderedReplicaTypes := orderedReplicaTypes(&job.Spec)
	for index := range nodeSelectors {
		replicaType := orderedReplicaTypes[index]
		nodeSelector := nodeSelectors[index]
		if len(nodeSelector) != 0 {
			if job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector == nil {
				job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = nodeSelector
			} else {
				for k, v := range nodeSelector {
					job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
				}
			}
		}
	}
	return nil
}

func (job *MPIJob) RestoreNodeAffinity(podSets []kueue.PodSet) error {
	orderedReplicaTypes := orderedReplicaTypes(&job.Spec)
	for index := range podSets {
		replicaType := orderedReplicaTypes[index]
		nodeSelector := podSets[index].Template.Spec.NodeSelector
		if !equality.Semantic.DeepEqual(job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector, nodeSelector) {
			job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = map[string]string{}
			for k, v := range nodeSelector {
				job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
			}
		}
	}
	return nil
}

func (job *MPIJob) Finished() (metav1.Condition, bool) {
	var conditionType kubeflow.JobConditionType
	var finished bool
	for _, c := range job.Status.Conditions {
		if (c.Type == kubeflow.JobSucceeded || c.Type == kubeflow.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}

	message := "Job finished successfully"
	if conditionType == kubeflow.JobFailed {
		message = "Job failed"
	}
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: message,
	}
	return condition, finished
}

func (job *MPIJob) EquivalentToWorkload(wl kueue.Workload) bool {
	if len(wl.Spec.PodSets) != len(job.Spec.MPIReplicaSpecs) {
		return false
	}
	for index, mpiReplicaType := range orderedReplicaTypes(&job.Spec) {
		mpiReplicaSpec := job.Spec.MPIReplicaSpecs[mpiReplicaType]
		if pointer.Int32Deref(mpiReplicaSpec.Replicas, 1) != wl.Spec.PodSets[index].Count {
			return false
		}
		// nodeSelector may change, hence we are not checking for
		// equality of the whole job.Spec.Template.Spec.
		if !equality.Semantic.DeepEqual(mpiReplicaSpec.Template.Spec.InitContainers,
			wl.Spec.PodSets[index].Template.Spec.InitContainers) {
			return false
		}
		if !equality.Semantic.DeepEqual(mpiReplicaSpec.Template.Spec.Containers,
			wl.Spec.PodSets[index].Template.Spec.Containers) {
			return false
		}
	}
	return true
}

// calcPriorityClassName calculates the priorityClass name needed for workload according to the following priorities:
//  1. .spec.runPolicy.schedulingPolicy.priorityClass
//  2. .spec.mpiReplicaSecs[Launcher].template.spec.priorityClassName
//  3. .spec.mpiReplicaSecs[Worker].template.spec.priorityClassName
//
// This function is inspired by an analogous one in mpi-controller:
// https://github.com/kubeflow/mpi-operator/blob/5946ef4157599a474ab82ff80e780d5c2546c9ee/pkg/controller/podgroup.go#L69-L72
func (job *MPIJob) PriorityClass() string {
	if job.Spec.RunPolicy.SchedulingPolicy != nil && len(job.Spec.RunPolicy.SchedulingPolicy.PriorityClass) != 0 {
		return job.Spec.RunPolicy.SchedulingPolicy.PriorityClass
	} else if l := job.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; l != nil && len(l.Template.Spec.PriorityClassName) != 0 {
		return l.Template.Spec.PriorityClassName
	} else if w := job.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; w != nil && len(w.Template.Spec.PriorityClassName) != 0 {
		return w.Template.Spec.PriorityClassName
	}
	return ""
}

func (job *MPIJob) PodsReady() bool {
	for _, c := range job.Status.Conditions {
		if c.Type == kubeflow.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *MPIJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeflow.MPIJob{}).
		Owns(&kueue.Workload{}).
		Complete(r)
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &kueue.Workload{}, jobframework.GetOwnerKey(&gvk), func(o client.Object) []string {
		// grab the Workload object, extract the owner...
		wl := o.(*kueue.Workload)
		owner := metav1.GetControllerOf(wl)
		if owner == nil {
			return nil
		}
		// ...make sure it's an MPIJob...
		if !jobframework.IsMPIJob(owner) {
			return nil
		}
		// ...and if so, return it
		return []string{owner.Name}
	})
}

//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=kubeflow.org,resources=mpijobs/status,verbs=get
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *MPIJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var job kubeflow.MPIJob
	if err := r.client.Get(ctx, req.NamespacedName, &job); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.genericJobReconciler.Reconcile(ctx, req, &MPIJob{job})
}

func orderedReplicaTypes(jobSpec *kubeflow.MPIJobSpec) []kubeflow.MPIReplicaType {
	var result []kubeflow.MPIReplicaType
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; ok {
		result = append(result, kubeflow.MPIReplicaTypeLauncher)
	}
	if _, ok := jobSpec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; ok {
		result = append(result, kubeflow.MPIReplicaTypeWorker)
	}
	return result
}

func podsCount(jobSpec *kubeflow.MPIJobSpec, mpiReplicaType kubeflow.MPIReplicaType) int32 {
	return pointer.Int32Deref(jobSpec.MPIReplicaSpecs[mpiReplicaType].Replicas, 1)
}

func GetWorkloadNameForMPIJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, &gvk)
}
