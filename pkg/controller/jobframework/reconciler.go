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

package jobframework

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errNodeSelectorsNotFound = fmt.Errorf("annotation %s not found", OriginalNodeSelectorsAnnotation)
)

// JobReconciler reconciles a GenericJob object
type JobReconciler struct {
	client                     client.Client
	scheme                     *runtime.Scheme
	record                     record.EventRecorder
	manageJobsWithoutQueueName bool
	waitForPodsReady           bool
}

type Options struct {
	ManageJobsWithoutQueueName bool
	WaitForPodsReady           bool
}

// Option configures the reconciler.
type Option func(*Options)

// WithManageJobsWithoutQueueName indicates if the controller should reconcile
// jobs that don't set the queue name annotation.
func WithManageJobsWithoutQueueName(f bool) Option {
	return func(o *Options) {
		o.ManageJobsWithoutQueueName = f
	}
}

// WithWaitForPodsReady indicates if the controller should add the PodsReady
// condition to the workload when the corresponding job has all pods ready
// or succeeded.
func WithWaitForPodsReady(f bool) Option {
	return func(o *Options) {
		o.WaitForPodsReady = f
	}
}

var DefaultOptions = Options{}

func NewReconciler(
	scheme *runtime.Scheme,
	client client.Client,
	record record.EventRecorder,
	opts ...Option) *JobReconciler {
	options := DefaultOptions
	for _, opt := range opts {
		opt(&options)
	}

	return &JobReconciler{
		scheme:                     scheme,
		client:                     client,
		record:                     record,
		manageJobsWithoutQueueName: options.ManageJobsWithoutQueueName,
		waitForPodsReady:           options.WaitForPodsReady,
	}
}

func (r *JobReconciler) ReconcileGenericJob(ctx context.Context, req ctrl.Request, job GenericJob) (ctrl.Result, error) {
	object := job.Object()
	if err := r.client.Get(ctx, req.NamespacedName, object); err != nil {
		// we'll ignore not-found errors, since there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	namespacedName := types.NamespacedName{Name: object.GetName(), Namespace: object.GetNamespace()}

	log := ctrl.LoggerFrom(ctx).WithValues("job", namespacedName.String())
	ctx = ctrl.LoggerInto(ctx, log)

	isStandaloneJob := ParentWorkloadName(job) == ""

	// when manageJobsWithoutQueueName is disabled we only reconcile jobs that have either
	// queue-name or the parent-workload annotation set.
	if !r.manageJobsWithoutQueueName && QueueName(job) == "" && isStandaloneJob {
		log.V(3).Info(fmt.Sprintf("Neither %s label, nor %s annotation is set, ignoring the job", QueueLabel, ParentWorkloadAnnotation))
		return ctrl.Result{}, nil
	}

	log.V(2).Info("Reconciling Job")

	// 1. make sure there is only a single existing instance of the workload.
	// If there's no workload exists and job is unsuspended, we'll stop it immediately.
	wl, err := r.ensureOneWorkload(ctx, job, object)
	if err != nil {
		log.Error(err, "Getting existing workloads")
		return ctrl.Result{}, err
	}

	// 2. handle job is finished.
	if condition, finished := job.Finished(); finished {
		if wl == nil || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			return ctrl.Result{}, nil
		}
		err := workload.UpdateStatus(ctx, r.client, wl, condition.Type, condition.Status, condition.Reason, condition.Message, constants.JobControllerName)
		if err != nil {
			log.Error(err, "Updating workload status")
		}
		return ctrl.Result{}, nil
	}

	// 3. handle workload is nil.
	if wl == nil {
		if !isStandaloneJob {
			return ctrl.Result{}, nil
		}
		err := r.handleJobWithNoWorkload(ctx, job, object)
		if err != nil {
			log.Error(err, "Handling job with no workload")
		}
		return ctrl.Result{}, err
	}

	// 4. update reclaimable counts.
	if rp := job.ReclaimablePods(); !workload.ReclaimablePodsAreEqual(rp, wl.Status.ReclaimablePods) {
		err = workload.UpdateReclaimablePods(ctx, r.client, wl, rp)
		if err != nil {
			log.Error(err, "Updating reclaimable pods")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 5. handle WaitForPodsReady only for a standalone job.
	if isStandaloneJob {
		// handle a job when waitForPodsReady is enabled, and it is the main job
		if r.waitForPodsReady {
			log.V(5).Info("Handling a job when waitForPodsReady is enabled")
			condition := generatePodsReadyCondition(job, wl)
			// optimization to avoid sending the update request if the status didn't change
			if !apimeta.IsStatusConditionPresentAndEqual(wl.Status.Conditions, condition.Type, condition.Status) {
				log.V(3).Info(fmt.Sprintf("Updating the PodsReady condition with status: %v", condition.Status))
				apimeta.SetStatusCondition(&wl.Status.Conditions, condition)
				err := workload.UpdateStatus(ctx, r.client, wl, condition.Type, condition.Status, condition.Reason, condition.Message, constants.JobControllerName)
				if err != nil {
					log.Error(err, "Updating workload status")
				}
			}
		}
	}

	// 6. handle eviction
	if evCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); evCond != nil && evCond.Status == metav1.ConditionTrue {
		if !job.IsSuspended() {
			log.V(6).Info("The job is not suspended, stop")
			return ctrl.Result{}, r.stopJob(ctx, job, object, wl, evCond.Message)
		}
		if workload.IsAdmitted(wl) {
			if !job.IsActive() {
				log.V(6).Info("The job is no longer active, clear the workloads admission")
				workload.UnsetAdmissionWithCondition(wl, "Pending", evCond.Message)
				return ctrl.Result{}, workload.ApplyAdmissionStatus(ctx, r.client, wl, true)
			}
			// The job is suspended but active, nothing to do now.
			return ctrl.Result{}, nil
		}
	}

	// 7. handle job is suspended.
	if job.IsSuspended() {
		// start the job if the workload has been admitted, and the job is still suspended
		if workload.IsAdmitted(wl) {
			log.V(2).Info("Job admitted, unsuspending")
			err := r.startJob(ctx, job, object, wl)
			if err != nil {
				log.Error(err, "Unsuspending job")
			}
			return ctrl.Result{}, err
		}

		// update queue name if changed.
		q := QueueName(job)
		if wl.Spec.QueueName != q && isStandaloneJob {
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

	// 8. handle job is unsuspended.
	if !workload.IsAdmitted(wl) {
		// the job must be suspended if the workload is not yet admitted.
		log.V(2).Info("Running job is not admitted by a cluster queue, suspending")
		err := r.stopJob(ctx, job, object, wl, "Not admitted by cluster queue")
		if err != nil {
			log.Error(err, "Suspending job with non admitted workload")
		}
		return ctrl.Result{}, err
	}

	// workload is admitted and job is running, nothing to do.
	log.V(3).Info("Job running with admitted workload, nothing to do")
	return ctrl.Result{}, nil
}

// ensureOneWorkload will query for the single matched workload corresponding to job and return it.
// If there're more than one workload, we should delete the excess ones.
// The returned workload could be nil.
func (r *JobReconciler) ensureOneWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)

	// Find a matching workload first if there is one.
	var toDelete []*kueue.Workload
	var match *kueue.Workload

	if pwName := ParentWorkloadName(job); pwName != "" {
		pw := kueue.Workload{}
		namespacedName := types.NamespacedName{
			Name:      pwName,
			Namespace: object.GetNamespace(),
		}
		if err := r.client.Get(ctx, namespacedName, &pw); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, err
			}
			log.V(2).Info("job with no matching parent workload", "parent-workload", pwName)
		} else {
			match = &pw
		}
	}

	var workloads kueue.WorkloadList
	if err := r.client.List(ctx, &workloads, client.InNamespace(object.GetNamespace()),
		client.MatchingFields{getOwnerKey(job.GetGVK()): object.GetName()}); err != nil {
		log.Error(err, "Unable to list child workloads")
		return nil, err
	}

	for i := range workloads.Items {
		w := &workloads.Items[i]
		if match == nil && r.equivalentToWorkload(job, object, w) {
			match = w
		} else {
			toDelete = append(toDelete, w)
		}
	}

	// If there is no matching workload and the job is running, suspend it.
	if match == nil && !job.IsSuspended() {
		log.V(2).Info("job with no matching workload, suspending")
		var w *kueue.Workload
		if len(workloads.Items) == 1 {
			// The job may have been modified and hence the existing workload
			// doesn't match the job anymore. All bets are off if there are more
			// than one workload...
			w = &workloads.Items[0]
		}
		if err := r.stopJob(ctx, job, object, w, "No matching Workload"); err != nil {
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
			r.record.Eventf(object, corev1.EventTypeNormal, "DeletedWorkload",
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

// equivalentToWorkload checks if the job corresponds to the workload
func (r *JobReconciler) equivalentToWorkload(job GenericJob, object client.Object, wl *kueue.Workload) bool {
	owner := metav1.GetControllerOf(wl)
	// Indexes don't work in unit tests, so we explicitly check for the
	// owner here.
	if owner.Name != object.GetName() {
		return false
	}
	return job.EquivalentToWorkload(*wl)
}

// startJob will unsuspend the job, and also inject the node affinity.
func (r *JobReconciler) startJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) error {
	//get the original selectors and store them in the job object
	originalSelectors := r.getNodeSelectorsFromPodSets(wl)
	if err := setNodeSelectorsInAnnotation(object, originalSelectors); err != nil {
		return fmt.Errorf("startJob, record original node selectors: %w", err)
	}

	nodeSelectors, err := r.getNodeSelectorsFromAdmission(ctx, wl)
	if err != nil {
		return err
	}
	job.RunWithNodeAffinity(nodeSelectors)

	if err := r.client.Update(ctx, object); err != nil {
		return err
	}

	r.record.Eventf(object, corev1.EventTypeNormal, "Started",
		"Admitted by clusterQueue %v", wl.Status.Admission.ClusterQueue)

	return nil
}

// stopJob will suspend the job, and also restore node affinity, reset job status if needed.
func (r *JobReconciler) stopJob(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload, eventMsg string) error {
	log := ctrl.LoggerFrom(ctx)
	// Suspend the job at first then we're able to update the scheduling directives.
	job.Suspend()

	if err := r.client.Update(ctx, object); err != nil {
		return err
	}

	r.record.Eventf(object, corev1.EventTypeNormal, "Stopped", eventMsg)

	if job.ResetStatus() {
		if err := r.client.Status().Update(ctx, object); err != nil {
			return err
		}
	}

	log.V(3).Info("restore node selectors from annotation")
	selectors, err := getNodeSelectorsFromObjectAnnotation(object)
	if err != nil {
		log.V(3).Error(err, "Unable to get original node selectors")
	} else {
		job.RestoreNodeAffinity(selectors)
		return r.client.Update(ctx, object)
	}

	return nil
}

// constructWorkload will derive a workload from the corresponding job.
func (r *JobReconciler) constructWorkload(ctx context.Context, job GenericJob, object client.Object) (*kueue.Workload, error) {
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GetWorkloadNameForOwnerWithGVK(object.GetName(), job.GetGVK()),
			Namespace: object.GetNamespace(),
		},
		Spec: kueue.WorkloadSpec{
			PodSets:   job.PodSets(),
			QueueName: QueueName(job),
		},
	}

	priorityClassName, p, err := utilpriority.GetPriorityFromPriorityClass(
		ctx, r.client, job.PriorityClass())
	if err != nil {
		return nil, err
	}

	wl.Spec.PriorityClassName = priorityClassName
	wl.Spec.Priority = &p

	if err := ctrl.SetControllerReference(object, wl, r.scheme); err != nil {
		return nil, err
	}
	return wl, nil
}

type PodSetNodeSelector struct {
	Name         string            `json:"name"`
	NodeSelector map[string]string `json:"nodeSelector"`
}

// getNodeSelectorsFromAdmission will extract node selectors from admitted workloads.
func (r *JobReconciler) getNodeSelectorsFromAdmission(ctx context.Context, w *kueue.Workload) ([]PodSetNodeSelector, error) {
	if len(w.Status.Admission.PodSetAssignments) == 0 {
		return nil, nil
	}

	nodeSelectors := make([]PodSetNodeSelector, len(w.Status.Admission.PodSetAssignments))

	for i, podSetFlavor := range w.Status.Admission.PodSetAssignments {
		processedFlvs := sets.NewString()
		nodeSelector := PodSetNodeSelector{
			Name:         podSetFlavor.Name,
			NodeSelector: make(map[string]string),
		}
		for _, flvRef := range podSetFlavor.Flavors {
			flvName := string(flvRef)
			if processedFlvs.Has(flvName) {
				continue
			}
			// Lookup the ResourceFlavors to fetch the node affinity labels to apply on the job.
			flv := kueue.ResourceFlavor{}
			if err := r.client.Get(ctx, types.NamespacedName{Name: string(flvName)}, &flv); err != nil {
				return nil, err
			}
			for k, v := range flv.Spec.NodeLabels {
				nodeSelector.NodeSelector[k] = v
			}
			processedFlvs.Insert(flvName)
		}

		nodeSelectors[i] = nodeSelector
	}
	return nodeSelectors, nil
}

// getNodeSelectorsFromPodSets will extract node selectors from a workload's podSets.
func (r *JobReconciler) getNodeSelectorsFromPodSets(w *kueue.Workload) []PodSetNodeSelector {
	podSets := w.Spec.PodSets
	if len(podSets) == 0 {
		return nil
	}
	ret := make([]PodSetNodeSelector, len(podSets))
	for psi := range podSets {
		ps := &podSets[psi]
		ret[psi] = PodSetNodeSelector{
			Name:         ps.Name,
			NodeSelector: cloneNodeSelector(ps.Template.Spec.NodeSelector),
		}
	}
	return ret
}

func (r *JobReconciler) handleJobWithNoWorkload(ctx context.Context, job GenericJob, object client.Object) error {
	log := ctrl.LoggerFrom(ctx)

	// Wait until there are no active pods.
	if job.IsActive() {
		log.V(2).Info("Job is suspended but still has active pods, waiting")
		return nil
	}

	// Create the corresponding workload.
	wl, err := r.constructWorkload(ctx, job, object)
	if err != nil {
		return err
	}
	if err = r.client.Create(ctx, wl); err != nil {
		return err
	}
	r.record.Eventf(object, corev1.EventTypeNormal, "CreatedWorkload",
		"Created Workload: %v", workload.Key(wl))
	return nil
}

func generatePodsReadyCondition(job GenericJob, wl *kueue.Workload) metav1.Condition {
	conditionStatus := metav1.ConditionFalse
	message := "Not all pods are ready or succeeded"
	// Once PodsReady=True it stays as long as the workload remains admitted to
	// avoid unnecessary flickering the the condition when the pods transition
	// Ready to Completed. As pods finish, they transition first into the
	// uncountedTerminatedPods staging area, before passing to the
	// succeeded/failed counters.
	if workload.IsAdmitted(wl) && (job.PodsReady() || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadPodsReady)) {
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

func cloneNodeSelector(src map[string]string) map[string]string {
	ret := make(map[string]string, len(src))
	for k, v := range src {
		ret[k] = v
	}
	return ret
}

// getNodeSelectorsFromObjectAnnotation tries to retrieve a node selectors slice from the
// object's annotations fails if it's not found or is unable to unmarshal
func getNodeSelectorsFromObjectAnnotation(obj client.Object) ([]PodSetNodeSelector, error) {
	str, found := obj.GetAnnotations()[OriginalNodeSelectorsAnnotation]
	if !found {
		return nil, errNodeSelectorsNotFound
	}
	// unmarshal
	ret := []PodSetNodeSelector{}
	if err := json.Unmarshal([]byte(str), &ret); err != nil {
		return nil, err
	}
	return ret, nil
}

// setNodeSelectorsInAnnotation - sets an annotation containing the provided node selectors into
// a job object, even if very unlikely it could return an error related to json.marshaling
func setNodeSelectorsInAnnotation(obj client.Object, nodeSelectors []PodSetNodeSelector) error {
	nodeSelectorsBytes, err := json.Marshal(nodeSelectors)
	if err != nil {
		return err
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{OriginalNodeSelectorsAnnotation: string(nodeSelectorsBytes)}
	} else {
		annotations[OriginalNodeSelectorsAnnotation] = string(nodeSelectorsBytes)
	}
	obj.SetAnnotations(annotations)
	return nil
}
