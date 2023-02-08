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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/jobframework"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ownerKey = ".metadata.mpijob_controller"
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

	log := ctrl.LoggerFrom(ctx).WithValues("mpijob", klog.KObj(&job))
	ctx = ctrl.LoggerInto(ctx, log)

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have
	// queue-name annotation set.
	if !r.manageJobsWithoutQueueName && queueName(&job) == "" {
		log.V(3).Info(fmt.Sprintf("%s annotation is not set, ignoring the mpijob", constants.QueueAnnotation))
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling MPIJob")

	// 1. make sure there is only a single existing instance of the workload
	wl, err := r.ensureAtMostOneWorkload(ctx, &job)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	// 2. handle mpijob is finished.
	if jobFinishedCond, jobFinished := jobFinishedCondition(&job); jobFinished {
		if wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}
		condition := generateFinishedCondition(jobFinishedCond)
		apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
		err := r.client.Status().Update(ctx, wl)
		if err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, err
	}

	// 3. handle workload is nil.
	if wl == nil {
		err := r.handleJobWithNoWorkload(ctx, &job)
		if err != nil {
			log.Error(err, "Handling mpijob with no workload")
		}
		return ctrl.Result{}, err
	}

	// 4. handle WaitForPodsReady
	if r.waitForPodsReady {
		log.V(5).Info("Handling a mpijob when waitForPodsReady is enabled")
		condition := generatePodsReadyCondition(&job, wl)
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
	if jobSuspended(&job) {
		// start the job if the workload has been admitted, and the job is still suspended
		if wl.Status.Admission != nil {
			log.V(2).Info("Job admitted, unsuspending")
			err := r.startJob(ctx, wl, &job)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// update queue name if changed.
		q := queueName(&job)
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
		err := r.stopJob(ctx, wl, &job, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

// podsReady checks if all pods are ready or succeeded
func podsReady(job *kubeflow.MPIJob) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == kubeflow.JobRunning && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// stopJob sends updates to suspend the job, reset the startTime so we can update the scheduling directives
// later when unsuspending and resets the nodeSelector to its previous state based on what is available in
// the workload (which should include the original affinities that the job had).
func (r *MPIJobReconciler) stopJob(ctx context.Context, w *kueue.Workload,
	job *kubeflow.MPIJob, eventMsg string) error {
	job.Spec.RunPolicy.Suspend = pointer.Bool(true)
	if err := r.client.Update(ctx, job); err != nil {
		return err
	}
	r.record.Eventf(job, corev1.EventTypeNormal, "Stopped", eventMsg)

	// Reset start time so we can update the scheduling directives later when unsuspending.
	if job.Status.StartTime != nil {
		job.Status.StartTime = nil
		if err := r.client.Status().Update(ctx, job); err != nil {
			return err
		}
	}

	if w != nil {
		orderedReplicaTypes := orderedReplicaTypes(&job.Spec)
		for index := range w.Spec.PodSets {
			replicaType := orderedReplicaTypes[index]
			if !equality.Semantic.DeepEqual(job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector,
				w.Spec.PodSets[index].Template.Spec.NodeSelector) {
				job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = map[string]string{}
				for k, v := range w.Spec.PodSets[index].Template.Spec.NodeSelector {
					job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
				}
			}
		}
		return r.client.Update(ctx, job)
	}

	return nil
}

func (r *MPIJobReconciler) startJob(ctx context.Context, w *kueue.Workload, job *kubeflow.MPIJob) error {
	log := ctrl.LoggerFrom(ctx)

	orderedReplicaTypes := orderedReplicaTypes(&job.Spec)
	for index := range w.Spec.PodSets {
		replicaType := orderedReplicaTypes[index]
		nodeSelector, err := r.getNodeSelectors(ctx, w, index)
		if err != nil {
			return err
		}
		if len(nodeSelector) != 0 {
			if job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector == nil {
				job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector = nodeSelector
			} else {
				for k, v := range nodeSelector {
					job.Spec.MPIReplicaSpecs[replicaType].Template.Spec.NodeSelector[k] = v
				}
			}
		} else {
			log.V(3).Info("no nodeSelectors to inject")
		}
	}

	job.Spec.RunPolicy.Suspend = pointer.Bool(false)
	if err := r.client.Update(ctx, job); err != nil {
		return err
	}

	r.record.Eventf(job, corev1.EventTypeNormal, "Started",
		"Admitted by clusterQueue %v", w.Status.Admission.ClusterQueue)
	return nil
}

func (r *MPIJobReconciler) getNodeSelectors(ctx context.Context, w *kueue.Workload, index int) (map[string]string, error) {
	if len(w.Status.Admission.PodSetFlavors[index].Flavors) == 0 {
		return nil, nil
	}

	processedFlvs := sets.New[kueue.ResourceFlavorReference]()
	nodeSelector := map[string]string{}
	for _, flvName := range w.Status.Admission.PodSetFlavors[index].Flavors {
		if processedFlvs.Has(flvName) {
			continue
		}
		// Lookup the ResourceFlavors to fetch the node affinity labels to apply on the job.
		flv := kueue.ResourceFlavor{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: string(flvName)}, &flv); err != nil {
			return nil, err
		}
		for k, v := range flv.Spec.NodeLabels {
			nodeSelector[k] = v
		}
		processedFlvs.Insert(flvName)
	}
	return nodeSelector, nil
}

func (r *MPIJobReconciler) handleJobWithNoWorkload(ctx context.Context, job *kubeflow.MPIJob) error {
	log := ctrl.LoggerFrom(ctx)

	// Wait until there are no active pods.
	for _, replicaStatus := range job.Status.ReplicaStatuses {
		if replicaStatus.Active != 0 {
			log.V(2).Info("Job is suspended but still has active pods, waiting")
			return nil
		}
	}

	// Create the corresponding workload.
	wl, err := ConstructWorkloadFor(ctx, r.client, job, r.scheme)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}

	r.record.Eventf(job, corev1.EventTypeNormal, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	return nil
}

// ensureAtMostOneWorkload finds a matching workload and deletes redundant ones.
func (r *MPIJobReconciler) ensureAtMostOneWorkload(ctx context.Context, job *kubeflow.MPIJob) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload

	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads, client.InNamespace(job.Namespace),
		client.MatchingFields{ownerKey: job.Name}); err != nil {
		log.Error(err, "Unable to list child workloads")
		return nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		owner := metav1.GetControllerOf(w)
		// Indexes don't work in unit tests, so we explicitly check for the
		// owner here.
		if owner.Name != job.Name {
			continue
		}
		if match == nil && jobAndWorkloadEqual(job, w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	// If there is no matching workload and the job is running, suspend it.
	if match == nil && !jobSuspended(job) {
		log.V(2).Info("job with no matching workload, suspending")
		var w *kueue.Workload
		if len(workloads.Items) == 1 {
			// The job may have been modified and hence the existing workload
			// doesn't match the job anymore. All bets are off if there are more
			// than one workload...
			w = &workloads.Items[0]
		}
		if err := r.stopJob(ctx, w, job, "No matching Workload"); err != nil {
			log.Error(err, "stopping job")
		}
	}

	// Delete duplicate workload instances.
	existedWls := 0
	for i := range toDelete {
		err := r.client.Delete(ctx, toDelete[i])
		if err == nil || !apierrors.IsNotFound(err) {
			existedWls++
		}
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete workload")
		}
		if err == nil {
			r.record.Eventf(job, corev1.EventTypeNormal, "DeletedWorkload",
				"Deleted not matching Workload: %v", workload.Key(toDelete[i]))
		}
	}

	if existedWls != 0 {
		if match == nil {
			return nil, fmt.Errorf("no matching workload was found, tried deleting %d existing workload(s)", existedWls)
		}
		return nil, fmt.Errorf("only one workload should exist, found %d", len(workloads.Items))
	}

	return match, nil
}

func ConstructWorkloadFor(ctx context.Context, client client.Client,
	job *kubeflow.MPIJob, scheme *runtime.Scheme) (*kueue.Workload, error) {
	w := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetWorkloadNameForMPIJob(job.Name),
			Namespace: job.Namespace,
		},
		Spec: kueue.WorkloadSpec{
			QueueName: queueName(job),
		},
	}

	for _, mpiReplicaType := range orderedReplicaTypes(&job.Spec) {
		podSet := kueue.PodSet{
			Name:     strings.ToLower(string(mpiReplicaType)),
			Template: *job.Spec.MPIReplicaSpecs[mpiReplicaType].Template.DeepCopy(),
			Count:    podsCount(&job.Spec, mpiReplicaType),
		}
		w.Spec.PodSets = append(w.Spec.PodSets, podSet)
	}

	// Populate priority from priority class.
	priorityClassName, p, err := utilpriority.GetPriorityFromPriorityClass(
		ctx, client, calcPriorityClassName(job))
	if err != nil {
		return nil, err
	}
	w.Spec.Priority = &p
	w.Spec.PriorityClassName = priorityClassName

	if err := ctrl.SetControllerReference(job, w, scheme); err != nil {
		return nil, err
	}

	return w, nil
}

// calcPriorityClassName calculates the priorityClass name needed for workload according to the following priorities:
//  1. .spec.runPolicy.schedulingPolicy.priorityClass
//  2. .spec.mpiReplicaSecs[Launcher].template.spec.priorityClassName
//  3. .spec.mpiReplicaSecs[Worker].template.spec.priorityClassName
//
// This function is inspired by an analogous one in mpi-controller:
// https://github.com/kubeflow/mpi-operator/blob/5946ef4157599a474ab82ff80e780d5c2546c9ee/pkg/controller/podgroup.go#L69-L72
func calcPriorityClassName(job *kubeflow.MPIJob) string {
	if job.Spec.RunPolicy.SchedulingPolicy != nil && len(job.Spec.RunPolicy.SchedulingPolicy.PriorityClass) != 0 {
		return job.Spec.RunPolicy.SchedulingPolicy.PriorityClass
	} else if l := job.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher]; l != nil && len(l.Template.Spec.PriorityClassName) != 0 {
		return l.Template.Spec.PriorityClassName
	} else if w := job.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker]; w != nil && len(w.Template.Spec.PriorityClassName) != 0 {
		return w.Template.Spec.PriorityClassName
	}
	return ""
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

func generatePodsReadyCondition(job *kubeflow.MPIJob, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the the condition.
	if wl.Status.Admission != nil && (podsReady(job) || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
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

func generateFinishedCondition(jobStatus kubeflow.JobConditionType) metav1.Condition {
	message := "Job finished successfully"
	if jobStatus == kubeflow.JobFailed {
		message = "Job failed"
	}
	return metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: message,
	}
}

// From https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/utils.go
func jobFinishedCondition(j *kubeflow.MPIJob) (kubeflow.JobConditionType, bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == kubeflow.JobSucceeded || c.Type == kubeflow.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Type, true
		}
	}
	return "", false
}

func jobSuspended(j *kubeflow.MPIJob) bool {
	return j.Spec.RunPolicy.Suspend != nil && *j.Spec.RunPolicy.Suspend
}

func jobAndWorkloadEqual(job *kubeflow.MPIJob, wl *kueue.Workload) bool {
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

func queueName(job *kubeflow.MPIJob) string {
	return job.Annotations[constants.QueueAnnotation]
}

func GetWorkloadNameForMPIJob(jobName string) string {
	gvk := metav1.GroupVersionKind{Group: kubeflow.SchemeGroupVersion.Group, Version: kubeflow.SchemeGroupVersion.Version, Kind: kubeflow.SchemeGroupVersionKind.Kind}
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, &gvk)
}
