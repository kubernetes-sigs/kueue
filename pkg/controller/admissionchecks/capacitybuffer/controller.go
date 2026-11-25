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

package capacitybuffer

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utilclient "sigs.k8s.io/kueue/pkg/util/client"
	equalityutil "sigs.k8s.io/kueue/pkg/util/equality"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	// admissionCheckLabelKey records which AdmissionCheck a CapacityBuffer was created for.
	admissionCheckLabelKey = "kueue.x-k8s.io/capacity-buffer-admission-check"
	// podSetLabelKey records which PodSet a CapacityBuffer was created for.
	podSetLabelKey = "kueue.x-k8s.io/capacity-buffer-podset"

	// ProvisioningStrategy is the only strategy supported by the CapacityBuffer API today.
	ProvisioningStrategy = "buffer.x-k8s.io/active-capacity"

	// The upstream API module does not export the condition names set by the autoscaler.
	ProvisioningCondition      = "Provisioning"
	ReasonFitsExistingCapacity = "FitsExistingCapacity"

	CheckInactiveMessage    = "the check is not active"
	NoBufferNeededMessage   = "the workload has no podsets requesting managed resources"
	buffersReadyMessage     = "CapacityBuffers fit existing capacity"
	buffersPendingMessage   = "waiting for CapacityBuffers to fit existing capacity"
	buffersTimedOutTemplate = "CapacityBuffers did not fit existing capacity within %ds"

	defaultBackoffBaseSeconds = 60
	defaultBackoffMaxSeconds  = 1800

	objNameMaxPrefixLength = 253 - objNameHashLength - 1
	objNameHashLength      = 5
)

var realClock = clock.RealClock{}

type capacityBufferConfigHelper = admissioncheck.ConfigHelper[*kueuealpha.CapacityBufferConfig, kueuealpha.CapacityBufferConfig]

type Controller struct {
	client      client.Client
	record      events.EventRecorder
	helper      *capacityBufferConfigHelper
	clock       clock.Clock
	roleTracker *roletracker.RoleTracker
}

var _ reconcile.Reconciler = (*Controller)(nil)

// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=capacitybuffers,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=capacitybuffers/status,verbs=get
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=capacitybufferconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=podtemplates,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch

func NewController(c client.Client, record events.EventRecorder, roleTracker *roletracker.RoleTracker) (*Controller, error) {
	helper, err := admissioncheck.NewConfigHelper[*kueuealpha.CapacityBufferConfig](c)
	if err != nil {
		return nil, err
	}
	return &Controller{
		client:      c,
		record:      record,
		helper:      helper,
		clock:       realClock,
		roleTracker: roleTracker,
	}, nil
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	wl := &kueue.Workload{}
	if err := c.client.Get(ctx, req.NamespacedName, wl); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(4).Info("Reconcile Workload")

	buffers, err := c.ownedCapacityBuffers(ctx, wl)
	if err != nil {
		return reconcile.Result{}, err
	}

	// The autoscaler keeps capacity for a CapacityBuffer until it is deleted, so
	// drop the buffers once the workload stops waiting for admission.
	if workloadfinish.IsFinished(wl) || workloadevict.IsEvicted(wl) || !workload.HasQuotaReservation(wl) {
		return reconcile.Result{}, c.deleteCapacityBuffers(ctx, buffers)
	}

	relevantChecks, err := admissioncheck.FilterForController(ctx, c.client, wl.Status.AdmissionChecks, kueuealpha.CapacityBufferControllerName)
	if err != nil {
		return reconcile.Result{}, err
	}

	relevant := sets.New(relevantChecks...)
	buffersForCheck := make(map[kueue.AdmissionCheckReference][]*autoscaling.CapacityBuffer, len(relevantChecks))
	var stale []*autoscaling.CapacityBuffer
	for _, cb := range buffers {
		check := kueue.AdmissionCheckReference(cb.Labels[admissionCheckLabelKey])
		if !relevant.Has(check) {
			stale = append(stale, cb)
			continue
		}
		buffersForCheck[check] = append(buffersForCheck[check], cb)
	}
	// Buffers of checks the workload no longer uses would keep their capacity
	// until the workload finishes.
	if err := c.deleteCapacityBuffers(ctx, stale); err != nil {
		return reconcile.Result{}, err
	}

	checksMap := slices.ToRefMap(wl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) kueue.AdmissionCheckReference { return c.Name })
	newStates := make([]kueue.AdmissionCheckState, 0, len(relevantChecks))
	var requeueAfter time.Duration
	var createErr error
	for _, check := range relevantChecks {
		checkState := *checksMap[check].DeepCopy()
		checkBuffers := buffersForCheck[check]

		// Releasing the buffers does not need the config, so an admitted workload
		// keeps its ready check and finishes starting if the config is deleted.
		// The autoscaler refills a buffer whose capacity is consumed, so it is
		// deleted rather than kept for the workload pods.
		// TODO: keep the buffers once capped buffers are supported upstream,
		// see KEP-5133.
		if checkState.State == kueue.CheckStateReady && workload.IsAdmitted(wl) {
			if err := c.releaseCapacityBuffers(ctx, wl, checkBuffers); err != nil {
				return reconcile.Result{}, err
			}
			continue
		}

		cbc, err := c.helper.ConfigForAdmissionCheck(ctx, check)
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		if cbc == nil {
			setCheckState(&checkState, kueue.CheckStatePending, CheckInactiveMessage)
			newStates = append(newStates, checkState)
			continue
		}

		// A ready check keeps its buffers until the workload is admitted, so that the
		// capacity is not released while other admission checks are pending. A Retry
		// check is reset to Pending when Kueue evicts the workload.
		if checkState.State != kueue.CheckStatePending {
			continue
		}

		newState, remaining, err := c.syncPendingCheck(ctx, wl, check, cbc, checkState, checkBuffers)
		newStates = append(newStates, newState)
		// The other checks are still evaluated, so that a timed out check releases
		// the quota while this one cannot create its buffers.
		createErr = errors.Join(createErr, err)
		if remaining > 0 && (requeueAfter == 0 || remaining < requeueAfter) {
			requeueAfter = remaining
		}
	}

	if err := c.syncCheckStates(ctx, wl, checksMap, newStates); err != nil {
		return reconcile.Result{}, err
	}
	// Returned after the sync so that the reconcile is retried with backoff.
	if createErr != nil {
		return reconcile.Result{}, createErr
	}
	// Skipped for workloads without a CapacityBuffer check. Removing the check from
	// the ClusterQueue while PodSets wait for their Pods leaves those Pods gated.
	if len(relevantChecks) > 0 && workload.IsAdmitted(wl) {
		if err := c.ungatePods(ctx, wl); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{RequeueAfter: requeueAfter}, nil
}

// syncPendingCheck returns the new state of the check and, while the check waits
// for its timeout, the time left until it expires.
func (c *Controller) syncPendingCheck(
	ctx context.Context,
	wl *kueue.Workload,
	check kueue.AdmissionCheckReference,
	cbc *kueuealpha.CapacityBufferConfig,
	checkState kueue.AdmissionCheckState,
	buffers []*autoscaling.CapacityBuffer,
) (kueue.AdmissionCheckState, time.Duration, error) {
	podSets := requiredPodSets(wl, cbc)
	if len(podSets) == 0 {
		setCheckState(&checkState, kueue.CheckStateReady, NoBufferNeededMessage)
		return checkState, 0, nil
	}

	// The timeout is measured from the quota reservation, so it also releases the
	// quota of a workload whose buffers cannot be created.
	remaining, hasTimeout := timeoutRemaining(cbc, wl, c.clock)
	if hasTimeout && remaining <= 0 && !buffersReadyForPodSets(podSets, buffers) {
		c.setTimedOut(&checkState, cbc)
		return checkState, 0, nil
	}

	buffers, err := c.ensureCapacityBuffers(ctx, wl, check, podSets, buffers)
	if foreignErr, ok := errors.AsType[*foreignObjectError](err); ok {
		c.record.Eventf(wl, nil, corev1.EventTypeWarning, "FailedCreate", "FailedCreate", foreignErr.Error())
		setCheckState(&checkState, kueue.CheckStatePending, foreignErr.Error())
		return checkState, 0, err
	}
	if err != nil {
		return checkState, 0, err
	}

	switch {
	case allBuffersReady(buffers):
		setCheckState(&checkState, kueue.CheckStateReady, buffersReadyMessage)
		checkState.PodSetUpdates = gatedPodSetUpdates(podSets)
	case hasTimeout && remaining <= 0:
		c.setTimedOut(&checkState, cbc)
	case hasTimeout:
		setCheckState(&checkState, kueue.CheckStatePending, buffersPendingMessage)
		return checkState, remaining, nil
	default:
		setCheckState(&checkState, kueue.CheckStatePending, buffersPendingMessage)
	}
	return checkState, 0, nil
}

func (c *Controller) syncCheckStates(ctx context.Context, wl *kueue.Workload, current map[kueue.AdmissionCheckReference]*kueue.AdmissionCheckState, newStates []kueue.AdmissionCheckState) error {
	changed := make([]kueue.AdmissionCheckState, 0, len(newStates))
	for _, s := range newStates {
		old := current[s.Name]
		if old.State != s.State {
			// s is a copy of the old state, and SetAdmissionCheckState keeps a
			// non-zero transition time.
			s.LastTransitionTime = metav1.NewTime(c.clock.Now())
		}
		if old.State != s.State || old.Message != s.Message {
			changed = append(changed, s)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	err := workloadpatching.PatchStatus(ctx, c.client, wl, kueuealpha.CapacityBufferControllerName, func(wlPatch *kueue.Workload) (bool, error) {
		// The SSA base patch does not include the admission checks, see the
		// provisioning controller. This copy can be removed once
		// WorkloadRequestUseMergePatch reaches GA.
		wlPatch.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, len(wl.Status.AdmissionChecks))
		for i := range wl.Status.AdmissionChecks {
			wlPatch.Status.AdmissionChecks[i] = *wl.Status.AdmissionChecks[i].DeepCopy()
		}
		for _, s := range changed {
			workloadpatching.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, s, c.clock)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	for _, s := range changed {
		c.record.Eventf(wl, nil, corev1.EventTypeNormal, "UpdatedAdmissionCheck", "UpdatedAdmissionCheck",
			"Admission check %s updated state to %s with message: %s", s.Name, s.State, s.Message)
	}
	return nil
}

func backoffBaseSeconds(cbc *kueuealpha.CapacityBufferConfig) int32 {
	if cbc.Spec.RetryStrategy == nil {
		return defaultBackoffBaseSeconds
	}
	return ptr.Deref(cbc.Spec.RetryStrategy.BackoffBaseSeconds, defaultBackoffBaseSeconds)
}

func backoffMaxSeconds(cbc *kueuealpha.CapacityBufferConfig) int32 {
	if cbc.Spec.RetryStrategy == nil {
		return defaultBackoffMaxSeconds
	}
	return ptr.Deref(cbc.Spec.RetryStrategy.BackoffMaxSeconds, defaultBackoffMaxSeconds)
}

func setCheckState(checkState *kueue.AdmissionCheckState, state kueue.CheckState, message string) {
	checkState.State = state
	checkState.Message = message
	checkState.PodSetUpdates = nil
	checkState.RequeueAfterSeconds = nil
}

func gatedPodSetUpdates(podSets []kueue.PodSetReference) []kueue.PodSetUpdate {
	updates := make([]kueue.PodSetUpdate, 0, len(podSets))
	for _, ps := range podSets {
		updates = append(updates, kueue.PodSetUpdate{
			Name:        ps,
			Annotations: map[string]string{kueuealpha.CapacityBufferGatedAnnotation: "true"},
		})
	}
	return updates
}

// Buffers are released per PodSet, so that PodSets whose Pods are created later, as with a
// JobSet InOrder startup policy, keep their capacity. A PodSet whose Pods never all appear
// keeps its buffer until the workload is evicted or finishes.
func (c *Controller) releaseCapacityBuffers(ctx context.Context, wl *kueue.Workload, buffers []*autoscaling.CapacityBuffer) error {
	assignments := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(psa *kueue.PodSetAssignment) kueue.PodSetReference {
		return psa.Name
	})
	podSets := slices.ToRefMap(wl.Spec.PodSets, func(ps *kueue.PodSet) kueue.PodSetReference { return ps.Name })

	var toDelete []*autoscaling.CapacityBuffer
	for _, cb := range buffers {
		psName := kueue.PodSetReference(cb.Labels[podSetLabelKey])
		ps, psa := podSets[psName], assignments[psName]
		if ps == nil || psa == nil {
			toDelete = append(toDelete, cb)
			continue
		}
		pods, err := c.podsForPodSet(ctx, wl, psName)
		if err != nil {
			return err
		}
		// Plain Pods exist before admission and cannot be gated, so ungated Pods count too.
		if int32(len(pods)) >= ptr.Deref(psa.Count, ps.Count) {
			toDelete = append(toDelete, cb)
		}
	}
	return c.deleteCapacityBuffers(ctx, toDelete)
}

// The gate stays in the Pod template while the workload is admitted, so Pods created later,
// such as replacements, are ungated too.
func (c *Controller) ungatePods(ctx context.Context, wl *kueue.Workload) error {
	buffers, err := c.ownedCapacityBuffers(ctx, wl)
	if err != nil {
		return err
	}
	buffered := sets.New[kueue.PodSetReference]()
	for _, cb := range buffers {
		buffered.Insert(kueue.PodSetReference(cb.Labels[podSetLabelKey]))
	}
	var toUngate []*corev1.Pod
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if buffered.Has(psa.Name) {
			continue
		}
		pods, err := c.podsForPodSet(ctx, wl, psa.Name)
		if err != nil {
			return err
		}
		for _, pod := range pods {
			if utilpod.HasGate(pod, kueuealpha.CapacityBufferSchedulingGate) {
				toUngate = append(toUngate, pod)
			}
		}
	}
	if len(toUngate) == 0 {
		return nil
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Ungating Pods", "count", len(toUngate))
	return parallelize.Until(ctx, len(toUngate), func(i int) error {
		pod := toUngate[i]
		return client.IgnoreNotFound(utilclient.Patch(ctx, c.client, pod, func() (bool, error) {
			return utilpod.Ungate(pod, kueuealpha.CapacityBufferSchedulingGate), nil
		}))
	})
}

func (c *Controller) podsForPodSet(ctx context.Context, wl *kueue.Workload, psName kueue.PodSetReference) ([]*corev1.Pod, error) {
	pods, err := workloadslicing.ListPodsForWorkloadSlice(ctx, c.client, wl.Namespace, workloadslicing.SliceName(wl),
		client.MatchingLabels{constants.PodSetLabel: string(psName)})
	if err != nil {
		return nil, err
	}
	return slices.Pick(pods, func(p **corev1.Pod) bool { return !utilpod.IsTerminated(*p) }), nil
}

// requiredPodSets returns the PodSets that need a CapacityBuffer: the ones
// with a non-zero assigned count that request at least one managed resource.
func requiredPodSets(wl *kueue.Workload, cbc *kueuealpha.CapacityBufferConfig) []kueue.PodSetReference {
	assignments := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(psa *kueue.PodSetAssignment) kueue.PodSetReference {
		return psa.Name
	})
	managed := sets.New(cbc.Spec.ManagedResources...)
	var required []kueue.PodSetReference
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		psa, found := assignments[ps.Name]
		if !found || ptr.Deref(psa.Count, ps.Count) <= 0 {
			continue
		}
		if managed.Len() == 0 || admissioncheck.PodUsesResources(&ps.Template.Spec, managed) {
			required = append(required, ps.Name)
		}
	}
	return required
}

func (c *Controller) ownedCapacityBuffers(ctx context.Context, wl *kueue.Workload) ([]*autoscaling.CapacityBuffer, error) {
	list := &autoscaling.CapacityBufferList{}
	if err := c.client.List(ctx, list, client.InNamespace(wl.Namespace), client.MatchingLabels{
		constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
	}); err != nil {
		return nil, err
	}
	var owned []*autoscaling.CapacityBuffer
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], wl) {
			owned = append(owned, &list.Items[i])
		}
	}
	return owned, nil
}

// ensureCapacityBuffers creates the missing CapacityBuffers, one per PodSet,
// and returns the buffers of the check.
func (c *Controller) ensureCapacityBuffers(
	ctx context.Context,
	wl *kueue.Workload,
	check kueue.AdmissionCheckReference,
	podSets []kueue.PodSetReference,
	existing []*autoscaling.CapacityBuffer,
) ([]*autoscaling.CapacityBuffer, error) {
	existingByPodSet := make(map[kueue.PodSetReference]*autoscaling.CapacityBuffer, len(existing))
	for _, cb := range existing {
		existingByPodSet[kueue.PodSetReference(cb.Labels[podSetLabelKey])] = cb
	}
	podSetsMap := slices.ToRefMap(wl.Spec.PodSets, func(ps *kueue.PodSet) kueue.PodSetReference { return ps.Name })
	assignments := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(psa *kueue.PodSetAssignment) kueue.PodSetReference {
		return psa.Name
	})

	buffers := make([]*autoscaling.CapacityBuffer, 0, len(podSets))
	for _, psName := range podSets {
		if cb, found := existingByPodSet[psName]; found {
			buffers = append(buffers, cb)
			continue
		}
		cb, err := c.createCapacityBuffer(ctx, wl, check, podSetsMap[psName], assignments[psName])
		if err != nil {
			return nil, err
		}
		buffers = append(buffers, cb)
	}
	return buffers, nil
}

func (c *Controller) createCapacityBuffer(
	ctx context.Context,
	wl *kueue.Workload,
	check kueue.AdmissionCheckReference,
	ps *kueue.PodSet,
	psa *kueue.PodSetAssignment,
) (*autoscaling.CapacityBuffer, error) {
	name := CapacityBufferName(wl.Name, check, ps.Name)
	labels := map[string]string{
		constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
		admissionCheckLabelKey:           string(check),
		podSetLabelKey:                   string(ps.Name),
	}

	pt, err := c.buildPodTemplate(ctx, wl, name, labels, ps, psa)
	if err != nil {
		return nil, err
	}
	if err := c.syncPodTemplate(ctx, wl, pt); err != nil {
		return nil, err
	}

	cb := &autoscaling.CapacityBuffer{
		Name:      name,
		Namespace: wl.Namespace,
		Labels:    labels,
		Spec: autoscaling.CapacityBufferSpec{
			ProvisioningStrategy: new(ProvisioningStrategy),
			PodTemplateRef:       &autoscaling.LocalObjectRef{Name: pt.Name},
			Replicas:             new(ptr.Deref(psa.Count, ps.Count)),
		},
	}
	if err := ctrl.SetControllerReference(wl, cb, c.client.Scheme()); err != nil {
		return nil, err
	}
	if err := c.client.Create(ctx, cb); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating CapacityBuffer: %w", err)
		}
		existing := &autoscaling.CapacityBuffer{}
		if getErr := c.client.Get(ctx, client.ObjectKeyFromObject(cb), existing); getErr != nil {
			// The cache has not observed the buffer an earlier reconcile created yet.
			return nil, errors.Join(err, client.IgnoreNotFound(getErr))
		}
		if !metav1.IsControlledBy(existing, wl) {
			return nil, &foreignObjectError{kind: "CapacityBuffer", name: cb.Name}
		}
		return existing, nil
	}

	ctrl.LoggerFrom(ctx).V(3).Info("Created CapacityBuffer", "capacityBuffer", klog.KObj(cb))
	c.record.Eventf(wl, nil, corev1.EventTypeNormal, "CapacityBufferCreated", "Create", "Created CapacityBuffer: %q", cb.Name)
	return cb, nil
}

// syncPodTemplate replaces the workload's own PodTemplate when it is stale, for
// example one left from an admission onto another flavor. A PodTemplate with the
// buffer's name that the workload does not own may belong to another user or
// workload, so it is not deleted.
func (c *Controller) syncPodTemplate(ctx context.Context, wl *kueue.Workload, desired *corev1.PodTemplate) error {
	existing := &corev1.PodTemplate{}
	err := c.client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err == nil {
		if !metav1.IsControlledBy(existing, wl) {
			return &foreignObjectError{kind: "PodTemplate", name: existing.Name}
		}
		if equalityutil.ComparePodTemplate(&existing.Template.Spec, &desired.Template.Spec) &&
			equality.Semantic.DeepEqual(existing.Template.Spec.NodeSelector, desired.Template.Spec.NodeSelector) &&
			equality.Semantic.DeepEqual(existing.Template.Spec.Affinity, desired.Template.Spec.Affinity) {
			return nil
		}
		if err := c.client.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting divergent PodTemplate: %w", err)
		}
		ctrl.LoggerFrom(ctx).V(3).Info("Replacing divergent PodTemplate", "podTemplate", klog.KObj(existing))
	}
	if err := c.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("creating PodTemplate: %w", err)
	}
	return nil
}

// foreignObjectError reports an object with a CapacityBuffer's name that the
// workload does not own, so its status cannot be trusted.
type foreignObjectError struct {
	kind string
	name string
}

func (e *foreignObjectError) Error() string {
	return fmt.Sprintf("%s %q already exists and is not owned by the workload", e.kind, e.name)
}

func (c *Controller) buildPodTemplate(
	ctx context.Context,
	wl *kueue.Workload,
	name string,
	labels map[string]string,
	ps *kueue.PodSet,
	psa *kueue.PodSetAssignment,
) (*corev1.PodTemplate, error) {
	pt := &corev1.PodTemplate{
		Name:      name,
		Namespace: wl.Namespace,
		Labels:    labels,
		// Deep-copy: podset.Merge mutates in place and ps.Template aliases wl.Spec.PodSets.
		Template: *ps.Template.DeepCopy(),
	}
	if err := ctrl.SetControllerReference(wl, pt, c.client.Scheme()); err != nil {
		return nil, err
	}

	// Apply the flavor node selectors and tolerations so that the capacity is
	// provisioned where the workload is going to run.
	psi, err := podset.FromAssignment(ctx, c.client, psa, ps)
	if err != nil {
		return nil, err
	}
	if err := podset.Merge(ctrl.LoggerFrom(ctx), &pt.Template.ObjectMeta, &pt.Template.Spec, psi); err != nil {
		return nil, err
	}
	workload.UseLimitsAsMissingRequestsInPod(&pt.Template.Spec)
	return pt, nil
}

// deleteCapacityBuffers deletes the buffers together with their PodTemplates,
// which share the buffer name.
func (c *Controller) deleteCapacityBuffers(ctx context.Context, buffers []*autoscaling.CapacityBuffer) error {
	for _, cb := range buffers {
		if err := c.client.Delete(ctx, cb); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting CapacityBuffer: %w", err)
		}
		pt := &corev1.PodTemplate{Name: cb.Name, Namespace: cb.Namespace}
		if err := c.client.Delete(ctx, pt); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting PodTemplate: %w", err)
		}
		ctrl.LoggerFrom(ctx).V(3).Info("Deleted CapacityBuffer", "capacityBuffer", klog.KObj(cb))
	}
	return nil
}

// allBuffersReady reports whether every buffer has Provisioning=True with
// reason FitsExistingCapacity.
func allBuffersReady(buffers []*autoscaling.CapacityBuffer) bool {
	for _, cb := range buffers {
		cond := apimeta.FindStatusCondition(cb.Status.Conditions, ProvisioningCondition)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonFitsExistingCapacity {
			return false
		}
	}
	return true
}

// buffersReadyForPodSets reports whether each of the PodSets has a buffer that
// fits existing capacity. Buffers of other PodSets, for example left after a
// config change, do not count.
func buffersReadyForPodSets(podSets []kueue.PodSetReference, buffers []*autoscaling.CapacityBuffer) bool {
	byPodSet := make(map[kueue.PodSetReference]*autoscaling.CapacityBuffer, len(buffers))
	for _, cb := range buffers {
		byPodSet[kueue.PodSetReference(cb.Labels[podSetLabelKey])] = cb
	}
	for _, ps := range podSets {
		cb, found := byPodSet[ps]
		if !found || !allBuffersReady([]*autoscaling.CapacityBuffer{cb}) {
			return false
		}
	}
	return true
}

// timeoutRemaining returns how long the check can still wait for its buffers,
// and false if the config has no timeout.
func timeoutRemaining(cbc *kueuealpha.CapacityBufferConfig, wl *kueue.Workload, clk clock.Clock) (time.Duration, bool) {
	if cbc.Spec.TimeoutSeconds == nil {
		return 0, false
	}
	timeout := time.Duration(*cbc.Spec.TimeoutSeconds) * time.Second
	return timeout - workload.QuotaReservedWaitTime(wl, clk), true
}

func (c *Controller) setTimedOut(checkState *kueue.AdmissionCheckState, cbc *kueuealpha.CapacityBufferConfig) {
	setCheckState(checkState, kueue.CheckStateRetry, fmt.Sprintf(buffersTimedOutTemplate, *cbc.Spec.TimeoutSeconds))
	// Without a backoff the workload would reserve quota again right away.
	workload.UpdateAdmissionCheckRequeueState(checkState, backoffBaseSeconds(cbc), backoffMaxSeconds(cbc), c.clock)
}

// CapacityBufferName returns the name of the CapacityBuffer, and of its
// PodTemplate, for the given workload, check and PodSet.
func CapacityBufferName(workloadName string, check kueue.AdmissionCheckReference, podSet kueue.PodSetReference) string {
	fullName := fmt.Sprintf("%s-%s-%s", workloadName, check, podSet)
	if len(fullName) <= objNameMaxPrefixLength {
		return fullName
	}
	h := sha1.New()
	h.Write([]byte(fullName))
	return fmt.Sprintf("%s-%s", fullName[:objNameMaxPrefixLength], hex.EncodeToString(h.Sum(nil))[:objNameHashLength])
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		Named("capacitybuffer_workload").
		For(&kueue.Workload{}).
		Owns(&autoscaling.CapacityBuffer{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(workloadForPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetAnnotations()[kueuealpha.CapacityBufferGatedAnnotation] == "true"
			}))).
		Watches(&kueue.AdmissionCheck{}, handler.EnqueueRequestsFromMapFunc(c.workloadsForAdmissionCheck)).
		Watches(
			&kueuealpha.CapacityBufferConfig{},
			handler.EnqueueRequestsFromMapFunc(c.workloadsForConfig),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(c.roleTracker, "capacitybuffer-workload-reconciler"),
		}).
		Complete(c)
	if err != nil {
		return err
	}

	acr := &acReconciler{
		client: c.client,
		helper: c.helper,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("capacitybuffer_admissioncheck").
		For(&kueue.AdmissionCheck{}).
		Watches(&kueuealpha.CapacityBufferConfig{}, handler.EnqueueRequestsFromMapFunc(acr.admissionChecksForConfig)).
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(c.roleTracker, "capacitybuffer-admissioncheck-reconciler"),
		}).
		Complete(acr)
}

func workloadForPod(_ context.Context, obj client.Object) []reconcile.Request {
	pod, isPod := obj.(*corev1.Pod)
	if !isPod {
		return nil
	}
	key := workloadslicing.KeyForPod(pod)
	if key == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: *key}}
}

// workloadsForAdmissionCheck requeues the workloads using a CapacityBuffer
// AdmissionCheck, for example when the check becomes active.
func (c *Controller) workloadsForAdmissionCheck(ctx context.Context, obj client.Object) []reconcile.Request {
	ac, isAc := obj.(*kueue.AdmissionCheck)
	if !isAc || ac.Spec.ControllerName != kueuealpha.CapacityBufferControllerName {
		return nil
	}
	return c.workloadsUsingChecks(ctx, ac.Name)
}

// workloadsForConfig requeues the workloads whose checks use the config, so that
// a created config or a changed timeout is applied to the waiting workloads.
func (c *Controller) workloadsForConfig(ctx context.Context, obj client.Object) []reconcile.Request {
	checks, err := admissionChecksUsingConfig(ctx, c.client, obj.GetName())
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Listing admission checks using the config", "capacityBufferConfig", klog.KObj(obj))
		return nil
	}
	return c.workloadsUsingChecks(ctx, checks...)
}

func (c *Controller) workloadsUsingChecks(ctx context.Context, checks ...string) []reconcile.Request {
	var requests []reconcile.Request
	for _, check := range checks {
		wls := &kueue.WorkloadList{}
		if err := c.client.List(ctx, wls, client.MatchingFields{indexer.WorkloadAdmissionCheckKey: check}); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing workloads using the admission check", "admissionCheck", check)
			continue
		}
		for i := range wls.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&wls.Items[i])})
		}
	}
	return requests
}
