/*
Copyright 2022 The Kubernetes Authors.

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

package job

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ownerKey = ".metadata.controller"
)

// JobReconciler reconciles a Job object
type JobReconciler struct {
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
	opts ...Option) *JobReconciler {

	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &JobReconciler{
		scheme:                     scheme,
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.manageJobsWithoutQueueName,
		waitForPodsReady:           options.waitForPodsReady,
	}
}

var _ GenericJob = &BatchJob{}

type BatchJob struct {
	batchv1.Job
}

func (b *BatchJob) Object() client.Object {
	return &b.Job
}

func (b *BatchJob) Ignored() bool {
	return len(b.QueueName()) == 0
}

func (b *BatchJob) QueueName() string {
	return queueName(&b.Job)
}

func (b *BatchJob) IsSuspend() bool {
	return b.Spec.Suspend != nil && *b.Spec.Suspend
}

func (b *BatchJob) Suspend() error {
	b.Spec.Suspend = pointer.Bool(true)
	return nil
}

func (b *BatchJob) UnSuspend() error {
	b.Spec.Suspend = pointer.Bool(false)
	return nil
}

func (b *BatchJob) ResetStatus() bool {
	// Reset start time so we can update the scheduling directives later when unsuspending.
	if b.Status.StartTime == nil {
		return false
	}
	b.Status.StartTime = nil
	return true
}

func (b *BatchJob) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Spec:  *b.Spec.Template.Spec.DeepCopy(),
			Count: b.podsCount(),
		},
	}
}

func (b *BatchJob) InjectNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 {
		return nil
	}

	if b.Spec.Template.Spec.NodeSelector == nil {
		b.Spec.Template.Spec.NodeSelector = nodeSelectors[0]
	} else {
		for k, v := range nodeSelectors[0] {
			b.Spec.Template.Spec.NodeSelector[k] = v
		}
	}

	return nil
}

func (b *BatchJob) RestoreNodeAffinity(nodeSelectors []map[string]string) error {
	if len(nodeSelectors) == 0 || equality.Semantic.DeepEqual(b.Spec.Template.Spec.NodeSelector, nodeSelectors) {
		return nil
	}

	b.Spec.Template.Spec.NodeSelector = map[string]string{}

	for k, v := range nodeSelectors[0] {
		b.Spec.Template.Spec.NodeSelector[k] = v
	}
	return nil
}

func (b *BatchJob) Finished() (metav1.Condition, bool) {
	var conditionType batchv1.JobConditionType
	var finished bool

	for _, c := range b.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			conditionType = c.Type
			finished = true
			break
		}
	}

	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: "Job finished successfully",
	}
	if conditionType == batchv1.JobFailed {
		condition.Message = "Job failed"
	}

	return condition, finished
}

func (b *BatchJob) EquivalentToWorkload(wl kueue.Workload) bool {
	owner := metav1.GetControllerOf(&wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != b.Name {
		return false
	}

	if len(wl.Spec.PodSets) != 1 {
		return false
	}

	if *b.Spec.Parallelism != wl.Spec.PodSets[0].Count {
		return false
	}

	// nodeSelector may change, hence we are not checking for
	// equality of the whole job.Spec.Template.Spec.
	if !equality.Semantic.DeepEqual(b.Spec.Template.Spec.InitContainers,
		wl.Spec.PodSets[0].Spec.InitContainers) {
		return false
	}
	return equality.Semantic.DeepEqual(b.Spec.Template.Spec.Containers,
		wl.Spec.PodSets[0].Spec.Containers)
}

func (b *BatchJob) PriorityClass() string {
	return b.Spec.Template.Spec.PriorityClassName
}

func (b *BatchJob) PodsReady() bool {
	ready := pointer.Int32Deref(b.Job.Status.Ready, 0)
	return b.Job.Status.Succeeded+ready >= b.podsCount()
}

func (b *BatchJob) podsCount() int32 {
	// parallelism is always set as it is otherwise defaulted by k8s to 1
	podsCount := *(b.Spec.Parallelism)
	if b.Spec.Completions != nil && *b.Spec.Completions < podsCount {
		podsCount = *b.Spec.Completions
	}
	return podsCount
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Owns(&kueue.Workload{}).
		Complete(r)
}

func SetupIndexes(indexer client.FieldIndexer) error {
	return indexer.IndexField(context.Background(), &kueue.Workload{}, ownerKey, func(o client.Object) []string {
		// grab the Workload object, extract the owner...
		wl := o.(*kueue.Workload)
		owner := metav1.GetControllerOf(wl)
		if owner == nil {
			return nil
		}
		// ...make sure it's a Job...
		if owner.APIVersion != "batch/v1" || owner.Kind != "Job" {
			return nil
		}
		// ...and if so, return it
		return []string{owner.Name}
	})
}

//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
//+kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
//+kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var batchJob BatchJob
	if err := r.client.Get(ctx, req.NamespacedName, &batchJob.Job); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("job", klog.KObj(&batchJob.Job))
	ctx = ctrl.LoggerInto(ctx, log)

	if !r.manageJobsWithoutQueueName {
		if batchJob.Ignored() {
			log.V(3).Info(fmt.Sprintf("%s annotation is not set, ignoring the job", constants.QueueAnnotation))
			return ctrl.Result{}, nil
		}
	}

	log.V(2).Info("Reconciling Job")

	wl, err := EnsureOneWorkload(ctx, r.client, req, r.record, &batchJob)
	if err != nil {
		log.Error(err, "Unable to find workload")
		return ctrl.Result{}, err
	}

	// Handing job is finished.
	if condition, finished := batchJob.Finished(); finished {
		if wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}

		if err := SetWorkloadCondition(ctx, r.client, wl, condition); err != nil {
			log.Error(err, "finishing workload")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Handing workload is nil.
	if wl == nil {
		if !batchJob.IsSuspend() {
			if err := StopJob(ctx, r.client, &batchJob, nil); err != nil {
				log.Error(err, "stopping batch job")
				return ctrl.Result{}, err
			}
			r.record.Eventf(&batchJob.Job, corev1.EventTypeNormal, "Stopped", "No matching workloads")
		}

		if batchJob.Status.Active > 0 && batchJob.IsSuspend() {
			return ctrl.Result{}, fmt.Errorf("job is suspended but still has active pods")
		}

		if wl, err = CreateWorkload(ctx, r.client, r.scheme, &batchJob); err != nil {
			log.Error(err, "creating workloads")
			return ctrl.Result{}, err
		}
		r.record.Eventf(&batchJob.Job, corev1.EventTypeNormal, "CreatedWorkload",
			"Created Workload: %v", workload.Key(wl))
		return ctrl.Result{}, nil
	}

	if r.waitForPodsReady {
		log.V(5).Info("Handling a job when waitForPodsReady is enabled")
		condition := metav1.Condition{
			Type:    kueue.WorkloadPodsReady,
			Status:  metav1.ConditionFalse,
			Reason:  "PodsReady",
			Message: "Not all pods are ready or succeeded",
		}
		if batchJob.PodsReady() && wl.Spec.Admission != nil {
			condition.Status = metav1.ConditionTrue
			condition.Message = "All pods are ready or succeeded"
		}
		// optimization to avoid sending the update request if the status didn't change
		if !apimeta.IsStatusConditionPresentAndEqual(wl.Status.Conditions, condition.Type, condition.Status) {
			log.V(3).Info(fmt.Sprintf("Updating the PodsReady condition with status: %v", condition.Status))
			if err := SetWorkloadCondition(ctx, r.client, wl, condition); err != nil {
				log.Error(err, "Updating workload status")
			}
		}
	}

	// Handing job is suspend.
	if batchJob.IsSuspend() {
		// start the job if the workload has been admitted, and the job is still suspended
		if wl.Spec.Admission != nil {
			log.V(2).Info("Job admitted, starting")
			err = StartJob(ctx, r.client, &batchJob, wl)
			if err != nil {
				log.Error(err, "starting batch job")
			}

			r.record.Eventf(&batchJob.Job, corev1.EventTypeNormal, "Started",
				"Admitted by clusterQueue %v", wl.Spec.Admission.ClusterQueue)
			return ctrl.Result{}, err
		}

		if err := UpdateQueueNameIfChanged(ctx, r.client, &batchJob, wl); err != nil {
			log.Error(err, "updating workload queueName")
			return ctrl.Result{}, err
		}
		log.V(3).Info("Job is suspended and workload not yet admitted by a clusterQueue")
		return ctrl.Result{}, nil
	}

	// Handing job is unsuspend.
	if wl.Spec.Admission == nil {
		// the job must be suspended if the workload is not yet admitted.
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		if err := StopJob(ctx, r.client, &batchJob, wl); err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		r.record.Eventf(&batchJob.Job, corev1.EventTypeNormal, "Stopped", "Not admitted by clusterQueue")
		return ctrl.Result{}, nil
	}
	log.V(3).Info("Job is running with admitted workload")

	return ctrl.Result{}, nil
}

func queueName(job *batchv1.Job) string {
	return job.Annotations[constants.QueueAnnotation]
}
