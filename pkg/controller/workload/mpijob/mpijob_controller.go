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
	"fmt"
	"strings"

	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/jobframework"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ownerKey = ".metadata.ownerReferences[kubeflow.MPIJob]"
)

// MPIJobReconciler reconciles a Job object
type MPIJobReconciler struct {
	client                     client.Client
	scheme                     *runtime.Scheme
	record                     record.EventRecorder
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
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

	return &MPIJobReconciler{
		scheme:                     scheme,
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.manageJobsWithoutQueueName,
		waitForPodsReady:           options.waitForPodsReady,
	}
}

type MPIJob struct {
	kubeflow.MPIJob
}

func (job *MPIJob) Object() client.Object {
	return &job.MPIJob
}

func (job *MPIJob) ParentWorkloadName() string {
	return job.MPIJob.Annotations[constants.ParentWorkloadAnnotation]
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

func (job *MPIJob) GetWorkloadName() string {
	return GetWorkloadNameForMPIJob(job.Name)
}

func (job *MPIJob) ResetStatus() bool {
	if job.Status.StartTime == nil {
		return false
	}
	job.Status.StartTime = nil
	return true
}

func (job *MPIJob) GetOwnerKey() string {
	return ownerKey
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
	owner := metav1.GetControllerOf(&wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != job.Name {
		return false
	}

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
	return indexer.IndexField(ctx, &kueue.Workload{}, ownerKey, func(o client.Object) []string {
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
	mpiJob := MPIJob{job}

	log := ctrl.LoggerFrom(ctx).WithValues("mpijob", klog.KObj(&job))
	ctx = ctrl.LoggerInto(ctx, log)

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have
	// queue-name annotation set.
	if !r.manageJobsWithoutQueueName && mpiJob.QueueName() == "" {
		log.V(3).Info(fmt.Sprintf("%s annotation is not set, ignoring the mpijob", constants.QueueAnnotation))
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling MPIJob")

	// 1. make sure there is only a single existing instance of the workload
	wl, err := jobframework.EnsureOneWorkload(ctx, r.client, req, r.record, &mpiJob)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if condition, finished := mpiJob.Finished(); finished {
		if wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}
		apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
		if err := r.client.Status().Update(ctx, wl); err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, nil
	}

	// 3. handle workload is nil.
	if wl == nil {
		err := r.handleJobWithNoWorkload(ctx, &mpiJob)
		if err != nil {
			log.Error(err, "Handling mpijob with no workload")
		}
		return ctrl.Result{}, err
	}

	// 4. handle WaitForPodsReady
	if r.waitForPodsReady {
		log.V(5).Info("Handling a mpijob when waitForPodsReady is enabled")
		condition := generatePodsReadyCondition(&mpiJob, wl)
		// optimization to avoid sending the update request if the status didn't change
		if !apimeta.IsStatusConditionPresentAndEqual(wl.Status.Conditions, condition.Type, condition.Status) {
			log.V(3).Info(fmt.Sprintf("Updating the PodsReady condition with status: %v", condition.Status))
			apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
			if err := r.client.Status().Update(ctx, wl); err != nil {
				log.Error(err, "Updating workload status")
			}
		}
	}

	// 5. handle mpijob is suspended.
	if mpiJob.IsSuspend() {
		// start the job if the workload has been admitted, and the job is still suspended
		if wl.Status.Admission != nil {
			log.V(2).Info("Job admitted, unsuspending")
			err := jobframework.StartJob(ctx, r.client, r.record, &mpiJob, wl)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// update queue name if changed.
		q := mpiJob.QueueName()
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

	// 6. handle job is unsuspended.
	if wl.Status.Admission == nil {
		// the job must be suspended if the workload is not yet admitted.
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		err := jobframework.StopJob(ctx, r.client, r.record, &mpiJob, wl, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

func (r *MPIJobReconciler) handleJobWithNoWorkload(ctx context.Context, mpiJob *MPIJob) error {
	log := ctrl.LoggerFrom(ctx)

	// Wait until there are no active pods.
	if mpiJob.IsActive() {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	// Create the corresponding workload.
	wl, err := jobframework.ConstructWorkload(ctx, r.client, r.scheme, mpiJob)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}
	r.record.Eventf(mpiJob.Object(), corev1.EventTypeNormal, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	return nil
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

func generatePodsReadyCondition(mpiJob *MPIJob, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the the condition.
	if wl.Status.Admission != nil && (mpiJob.PodsReady() || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
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

func GetWorkloadNameForMPIJob(jobName string) string {
	gvk := metav1.GroupVersionKind{Group: kubeflow.SchemeGroupVersion.Group, Version: kubeflow.SchemeGroupVersion.Version, Kind: kubeflow.SchemeGroupVersionKind.Kind}
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, &gvk)
}
