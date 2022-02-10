/*
Copyright 2022 Google LLC.

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

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

var (
	ownerKey        = ".metadata.controller"
	queueAnnotation = "controller.kubernetes.io/queue-name"
)

// JobReconciler reconciles a Job object
type JobReconciler struct {
	client client.Client
	scheme *runtime.Scheme
	log    logr.Logger
}

func NewJobReconciler(scheme *runtime.Scheme, client client.Client) *JobReconciler {
	return &JobReconciler{
		log:    ctrl.Log.WithName("job-reconciler"),
		scheme: scheme,
		client: client,
	}
}

// SetupWithManager sets up the controller with the Manager. It indexes workloads
// based on the owning jobs.
func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &kueue.QueuedWorkload{}, ownerKey, func(rawObj client.Object) []string {
		// grab the QueuedWorkload object, extract the owner...
		qw := rawObj.(*kueue.QueuedWorkload)
		owner := metav1.GetControllerOf(qw)
		if owner == nil {
			return nil
		}
		// ...make sure it's a Job...
		if owner.APIVersion != "batch/v1" || owner.Kind != "Job" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Owns(&kueue.QueuedWorkload{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kueue.gke-internal.googlesource.com,resources=queuedworkloads/finalizers,verbs=update

func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var job batchv1.Job
	if err := r.client.Get(ctx, req.NamespacedName, &job); err != nil {
		r.log.Error(err, "unable to fetch Job")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := r.log.WithValues("job", klog.KObj(&job))
	log.V(2).Info("Job reconcile event")

	var childWorkloads kueue.QueuedWorkloadList
	if err := r.client.List(ctx, &childWorkloads, client.InNamespace(req.Namespace),
		client.MatchingFields{ownerKey: req.Name}); err != nil {
		log.Error(err, "Unable to list child workloads")
		return ctrl.Result{}, err
	}

	// 1. make sure there is only a single existing instance of the workload
	wl, err := r.ensureAtMostOneWorkload(ctx, log, &job, childWorkloads)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	jobFinishedCond, jobFinished := jobFinishedCondition(&job)
	// 2. create new workload if none exists
	if wl == nil {
		// Nothing to do if the job is finished
		if jobFinished {
			return ctrl.Result{}, nil
		}
		err := r.handleJobWithNoWorkload(ctx, log, &job)
		if err != nil {
			log.Error(err, "Handling job with no workload")
		}
		return ctrl.Result{}, err
	}

	// 3. handle a finished job
	if jobFinished {
		wlFinishedCond := newFinishedCondition(jobFinishedCond)
		wl.Status.Conditions = append(wl.Status.Conditions, *wlFinishedCond)
		err := r.client.Status().Update(ctx, wl)
		if err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, err
	}

	// 4. Handle a not finished job
	if jobSuspended(&job) {
		// 4.1 start the job if the workload has been assigned, and the job is still suspended
		if wl.Spec.AssignedCapacity != "" {
			log.V(2).Info("Job assigned a capacity, unsuspending")
			job.Spec.Suspend = pointer.BoolPtr(false)
			err := r.client.Update(ctx, &job)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// 4.2 update queue name if changed.
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
		log.V(3).Info("Job is suspended and workload not yet assigned a capacity, nothing to do")
		return ctrl.Result{}, nil
	}

	if wl.Spec.AssignedCapacity == "" {
		// 4.3 the job must be suspended if the workload is not yet assigned a capacity.
		log.V(2).Info("Job running with no assigned capacity, suspending")
		job.Spec.Suspend = pointer.BoolPtr(true)
		err := r.client.Update(ctx, &job)
		if err != nil {
			log.Error(err, "Suspending job with unassigned workload")
		}
		return ctrl.Result{}, err
	}

	// 4.4 workload is assigned and job is running, nothing to do.
	log.V(3).Info("Job running with an assigned capacity, nothing to do")
	return ctrl.Result{}, nil

}

func (r *JobReconciler) handleJobWithNoWorkload(ctx context.Context, log logr.Logger, job *batchv1.Job) error {
	// If the job is running, suspend it.
	if !jobSuspended(job) {
		log.V(2).Info("Job running with no corresponding workload object, suspending")
		job.Spec.Suspend = pointer.BoolPtr(true)
		return r.client.Update(ctx, job)
	}

	// Wait until there are no active pods.
	if job.Status.Active != 0 {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	// Create the corresponding workload.
	wl, err := constructWorkloadFromJob(job, r.scheme)
	if err != nil {
		return err
	}
	return r.client.Create(ctx, wl)
}

// ensureAtmostoneworkload finds a matching workload and deletes redundant ones.
func (r *JobReconciler) ensureAtMostOneWorkload(ctx context.Context, log logr.Logger, job *batchv1.Job, workloads kueue.QueuedWorkloadList) (*kueue.QueuedWorkload, error) {
	if len(workloads.Items) == 0 {
		return nil, nil
	}

	// Find a matching workload first if there is one.
	var toDelete []*kueue.QueuedWorkload
	var match *kueue.QueuedWorkload
	for i := range workloads.Items {
		w := &workloads.Items[i]
		owner := metav1.GetControllerOf(w)
		// Indexes don't work in unit tests, so we explicity check for the
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

	// Delete redundant workload instances.
	for i := range toDelete {
		if err := r.client.Delete(ctx, toDelete[i]); err != nil {
			log.Error(err, "Failed to delete workload")
		}
	}

	if len(toDelete) != 0 {
		return nil, fmt.Errorf("only one workload should exist, found %d", len(workloads.Items))
	}

	return match, nil
}

func constructWorkloadFromJob(job *batchv1.Job, scheme *runtime.Scheme) (*kueue.QueuedWorkload, error) {
	w := &kueue.QueuedWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      job.Name,
			Namespace: job.Namespace,
		},
		Spec: kueue.QueuedWorkloadSpec{
			Pods: []kueue.PodSet{
				{
					Spec:  *job.Spec.Template.Spec.DeepCopy(),
					Count: *job.Spec.Parallelism,
				},
			},
			QueueName: queueName(job),
		},
	}
	if err := ctrl.SetControllerReference(job, w, scheme); err != nil {
		return nil, err
	}

	return w, nil
}

func newFinishedCondition(jobStatus batchv1.JobConditionType) *kueue.QueuedWorkloadCondition {
	message := "Job finished successfully"
	if jobStatus == batchv1.JobFailed {
		message = "Job failed"
	}
	now := metav1.Now()
	return &kueue.QueuedWorkloadCondition{
		Type:               kueue.QueuedWorkloadFinished,
		Status:             corev1.ConditionTrue,
		LastProbeTime:      now,
		LastTransitionTime: now,
		Reason:             "JobFinished",
		Message:            message,
	}
}

// From https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/utils.go
func jobFinishedCondition(j *batchv1.Job) (batchv1.JobConditionType, bool) {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Type, true
		}
	}
	return "", false
}

func jobSuspended(j *batchv1.Job) bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend

}

func jobAndWorkloadEqual(job *batchv1.Job, wl *kueue.QueuedWorkload) bool {
	if len(wl.Spec.Pods) != 1 {
		return false
	}
	if *job.Spec.Parallelism != wl.Spec.Pods[0].Count {
		return false
	}

	return equality.Semantic.DeepEqual(job.Spec.Template.Spec, wl.Spec.Pods[0].Spec)
}

func queueName(job *batchv1.Job) string {
	return job.Annotations[queueAnnotation]
}
