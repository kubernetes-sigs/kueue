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

package provisioning

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	objNameHashLength = 5
	// 253 is the maximal length for a CRD name. We need to subtract one for '-', and the hash length.
	objNameMaxPrefixLength = 252 - objNameHashLength
	podTemplatesPrefix     = "ppt"
)

var (
	errInconsistentPodSetAssignments = errors.New("inconsistent podSet assignments")
)

var (
	realClock = clock.RealClock{}
)

type provisioningConfigHelper = admissioncheck.ConfigHelper[*kueue.ProvisioningRequestConfig, kueue.ProvisioningRequestConfig]

func newProvisioningConfigHelper(c client.Client) (*provisioningConfigHelper, error) {
	return admissioncheck.NewConfigHelper[*kueue.ProvisioningRequestConfig](c)
}

type Controller struct {
	client client.Client
	record record.EventRecorder
	helper *provisioningConfigHelper
	clock  clock.Clock
}

type workloadInfo struct {
	checkStates  []kueue.AdmissionCheckState
	requeueState *kueue.RequeueState
}

var _ reconcile.Reconciler = (*Controller)(nil)

// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update
// +kubebuilder:rbac:groups="",resources=podtemplates,verbs=get;list;watch;create;delete;update
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=provisioningrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=provisioningrequests/status,verbs=get
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=provisioningrequestconfigs,verbs=get;list;watch

func NewController(client client.Client, record record.EventRecorder) (*Controller, error) {
	helper, err := newProvisioningConfigHelper(client)
	if err != nil {
		return nil, err
	}
	return &Controller{
		client: client,
		record: record,
		helper: helper,
		clock:  realClock,
	}, nil
}

// Reconcile performs a full reconciliation for the object referred to by the Request.
// The Controller will requeue the Request to be processed again if an error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	wl := &kueue.Workload{}

	err := c.client.Get(ctx, req.NamespacedName, wl)
	if err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile Workload")

	if !workload.HasQuotaReservation(wl) || workload.IsFinished(wl) || workload.IsEvicted(wl) {
		return reconcile.Result{}, nil
	}

	provReqs := &autoscaling.ProvisioningRequestList{}
	if err := c.client.List(ctx, provReqs, client.InNamespace(wl.Namespace), client.MatchingFields{RequestsOwnedByWorkloadKey: wl.Name}); client.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, err
	}

	// get the lists of relevant checks
	relevantChecks, err := admissioncheck.FilterForController(ctx, c.client, wl.Status.AdmissionChecks, kueue.ProvisioningRequestControllerName)
	if err != nil {
		return reconcile.Result{}, err
	}

	checkConfig := make(map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig, len(relevantChecks))
	for _, checkName := range relevantChecks {
		prc, err := c.helper.ConfigForAdmissionCheck(ctx, checkName)
		if client.IgnoreNotFound(err) != nil {
			return reconcile.Result{}, err
		}
		checkConfig[checkName] = prc
	}

	activeOrLastPRForChecks := c.activeOrLastPRForChecks(ctx, wl, checkConfig, provReqs.Items)

	wlInfo := workloadInfo{
		checkStates: make([]kueue.AdmissionCheckState, 0),
	}
	err = c.syncCheckStates(ctx, wl, &wlInfo, checkConfig, activeOrLastPRForChecks)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = c.deleteUnusedProvisioningRequests(ctx, provReqs.Items, activeOrLastPRForChecks)
	if err != nil {
		log.V(2).Error(err, "syncOwnedProvisionRequest failed to delete unused provisioning requests")
		return reconcile.Result{}, err
	}

	err = c.syncOwnedProvisionRequest(ctx, wl, &wlInfo, checkConfig, activeOrLastPRForChecks)
	if err != nil {
		// this can also delete unneeded checks
		log.V(2).Error(err, "syncOwnedProvisionRequest failed")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (c *Controller) activeOrLastPRForChecks(
	ctx context.Context,
	wl *kueue.Workload,
	checkConfig map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig,
	ownedPRs []autoscaling.ProvisioningRequest,
) map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest {
	activeOrLastPRForChecks := make(map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest)
	log := ctrl.LoggerFrom(ctx)
	for checkName, prc := range checkConfig {
		if prc == nil {
			continue
		}
		for i := range ownedPRs {
			req := &ownedPRs[i]
			// PRs relevant for the admission check
			if matchesWorkloadAndCheck(req, wl.Name, checkName) {
				if c.reqIsNeeded(wl, prc) && provReqSyncedWithConfig(req, prc) {
					currPr, exists := activeOrLastPRForChecks[checkName]
					if !exists || getAttempt(log, currPr, wl.Name, checkName) < getAttempt(log, req, wl.Name, checkName) {
						activeOrLastPRForChecks[checkName] = req
					}
				}
			}
		}
	}
	return activeOrLastPRForChecks
}

func (c *Controller) deleteUnusedProvisioningRequests(ctx context.Context, ownedPRs []autoscaling.ProvisioningRequest, activeOrLastPRForChecks map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest) error {
	log := ctrl.LoggerFrom(ctx)
	prNames := sets.New[string]()
	for _, pr := range activeOrLastPRForChecks {
		prNames.Insert(pr.Name)
	}
	for _, pr := range ownedPRs {
		req := &pr
		if !prNames.Has(req.Name) {
			if err := c.client.Delete(ctx, req); client.IgnoreNotFound(err) != nil {
				log.V(5).Error(err, "deleting the request", "req", klog.KObj(req))
				return err
			}
		}
	}
	return nil
}

func (c *Controller) syncOwnedProvisionRequest(
	ctx context.Context,
	wl *kueue.Workload,
	wlInfo *workloadInfo,
	checkConfig map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig,
	activeOrLastPRForChecks map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest,
) error {
	log := ctrl.LoggerFrom(ctx)
	for checkName, prc := range checkConfig {
		if prc == nil {
			// the check is not active
			continue
		}
		if !c.reqIsNeeded(wl, prc) {
			continue
		}
		ac := workload.FindAdmissionCheck(wlInfo.checkStates, checkName)
		if ac != nil && ac.State == kueue.CheckStateReady {
			log.V(2).Info("Skip syncing of the ProvReq for admission check which is Ready", "workload", klog.KObj(wl), "admissionCheck", checkName)
			continue
		}

		req, exists := activeOrLastPRForChecks[checkName]
		attempt := int32(1)
		shouldCreatePr := false
		if exists {
			if (isFailed(req) || (isBookingExpired(req) && !workload.IsAdmitted(wl))) &&
				// if the workload is Admitted we don't want to retry on BookingExpired
				ac != nil && ac.State == kueue.CheckStatePending {
				// if the workload is in Retry/Rejected state we don't create another ProvReq
				attempt = getAttempt(log, req, wl.Name, checkName)
				shouldCreatePr = true
				attempt++
			}
		} else {
			shouldCreatePr = true
		}
		requestName := ProvisioningRequestName(wl.Name, checkName, attempt)
		if shouldCreatePr {
			log.V(3).Info("Creating ProvisioningRequest", "requestName", requestName, "attempt", attempt)
			req = &autoscaling.ProvisioningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      requestName,
					Namespace: wl.Namespace,
					Labels: map[string]string{
						constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
					},
				},
				Spec: autoscaling.ProvisioningRequestSpec{
					ProvisioningClassName: prc.Spec.ProvisioningClassName,
					Parameters:            parametersKueueToProvisioning(prc.Spec.Parameters),
				},
			}
			passProvReqParams(wl, req)

			mergedPodSets, err := mergePodSets(wl, &prc.Spec)
			if err != nil {
				return err
			}

			for _, mergedPodSet := range mergedPodSets {
				ptName := getProvisioningRequestPodTemplateName(requestName, mergedPodSet.Name)

				pt := &corev1.PodTemplate{}
				err := c.client.Get(ctx, types.NamespacedName{Namespace: wl.Namespace, Name: ptName}, pt)
				if client.IgnoreNotFound(err) != nil {
					return err
				}
				if err != nil {
					// it's a not found, so create it
					_, err := c.createPodTemplate(ctx, wl, ptName, mergedPodSet.PodSet, mergedPodSet.PodSetAssignment)
					if err != nil {
						msg := fmt.Sprintf("Error creating PodTemplate %q: %v", ptName, err)
						return c.handleError(ctx, wl, ac, msg, err)
					}
				}

				req.Spec.PodSets = append(req.Spec.PodSets, autoscaling.PodSet{
					PodTemplateRef: autoscaling.Reference{
						Name: ptName,
					},
					Count: mergedPodSet.Count,
				})
			}

			if err := ctrl.SetControllerReference(wl, req, c.client.Scheme()); err != nil {
				return err
			}

			if err := c.client.Create(ctx, req); err != nil {
				msg := fmt.Sprintf("Error creating ProvisioningRequest %q: %v", requestName, err)
				return c.handleError(ctx, wl, ac, msg, err)
			}
			c.record.Eventf(wl, corev1.EventTypeNormal, "ProvisioningRequestCreated", "Created ProvisioningRequest: %q", req.Name)
			activeOrLastPRForChecks[checkName] = req
		}
		if err := c.syncProvisionRequestsPodTemplates(ctx, wl, req); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) handleError(ctx context.Context, wl *kueue.Workload, ac *kueue.AdmissionCheckState, msg string, err error) error {
	c.record.Eventf(wl, corev1.EventTypeWarning, "FailedCreate", api.TruncateEventMessage(msg))

	ac.Message = api.TruncateConditionMessage(msg)
	wlPatch := workload.BaseSSAWorkload(wl, true)
	workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, *ac, c.clock)

	patchErr := c.client.Status().Patch(
		ctx, wlPatch, client.Apply,
		client.FieldOwner(kueue.ProvisioningRequestControllerName),
		client.ForceOwnership,
	)

	return errors.Join(err, patchErr)
}

func (c *Controller) createPodTemplate(ctx context.Context, wl *kueue.Workload, name string, ps *kueue.PodSet, psa *kueue.PodSetAssignment) (*corev1.PodTemplate, error) {
	newPt := &corev1.PodTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: wl.Namespace,
			Labels: map[string]string{
				constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
			},
		},
		Template: ps.Template,
	}

	// set the controller reference to workload so that the template is not left orphaned
	// if the ProvisioningRequest creation fails. The ownership is later transferred to the
	// ProvisioningRequest.
	if err := ctrl.SetControllerReference(wl, newPt, c.client.Scheme()); err != nil {
		return nil, err
	}

	// apply the admission node selectors to the Template
	psi, err := podset.FromAssignment(ctx, c.client, psa, ptr.Deref(psa.Count, ps.Count))
	if err != nil {
		return nil, err
	}

	err = podset.Merge(&newPt.Template.ObjectMeta, &newPt.Template.Spec, psi)
	if err != nil {
		return nil, err
	}

	// copy limits to requests if needed
	workload.UseLimitsAsMissingRequestsInPod(&newPt.Template.Spec)

	if err := c.client.Create(ctx, newPt); err != nil {
		return nil, err
	}

	return newPt, nil
}

func (c *Controller) syncProvisionRequestsPodTemplates(ctx context.Context, wl *kueue.Workload, request *autoscaling.ProvisioningRequest) error {
	for i := range request.Spec.PodSets {
		reqPS := &request.Spec.PodSets[i]

		pt := &corev1.PodTemplate{}
		ptKey := types.NamespacedName{
			Namespace: request.Namespace,
			Name:      reqPS.PodTemplateRef.Name,
		}

		err := c.client.Get(ctx, ptKey, pt)
		if client.IgnoreNotFound(err) != nil {
			return err
		}

		if err == nil {
			var shouldUpdate bool

			// transfer the ownership of the template to the ProvisioningRequest
			if metav1.IsControlledBy(pt, wl) {
				if err := controllerutil.RemoveControllerReference(wl, pt, c.client.Scheme()); err != nil {
					return err
				}
				shouldUpdate = true
			}

			if !metav1.IsControlledBy(pt, request) {
				if err := controllerutil.SetControllerReference(request, pt, c.client.Scheme()); err != nil {
					return err
				}
				shouldUpdate = true
			}

			if shouldUpdate {
				if err := c.client.Update(ctx, pt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Controller) reqIsNeeded(wl *kueue.Workload, prc *kueue.ProvisioningRequestConfig) bool {
	return len(requiredPodSets(wl.Spec.PodSets, prc.Spec.ManagedResources)) > 0
}

func requiredPodSets(podSets []kueue.PodSet, resources []corev1.ResourceName) []kueue.PodSetReference {
	resourcesSet := sets.New(resources...)
	users := make([]kueue.PodSetReference, 0, len(podSets))
	for i := range podSets {
		ps := &podSets[i]
		if len(resources) == 0 || podUses(&ps.Template.Spec, resourcesSet) {
			users = append(users, ps.Name)
		}
	}
	return users
}

func podUses(pod *corev1.PodSpec, resourceSet sets.Set[corev1.ResourceName]) bool {
	for i := range pod.InitContainers {
		if containerUses(&pod.InitContainers[i], resourceSet) {
			return true
		}
	}
	for i := range pod.Containers {
		if containerUses(&pod.Containers[i], resourceSet) {
			return true
		}
	}
	return false
}

func containerUses(cont *corev1.Container, resourceSet sets.Set[corev1.ResourceName]) bool {
	for r := range cont.Resources.Requests {
		if resourceSet.Has(r) {
			return true
		}
	}
	for r := range cont.Resources.Limits {
		if resourceSet.Has(r) {
			return true
		}
	}
	return false
}

func updateCheckMessage(checkState *kueue.AdmissionCheckState, message string) bool {
	if message == "" || checkState.Message == message {
		return false
	}
	checkState.Message = message
	return true
}

func updateCheckState(checkState *kueue.AdmissionCheckState, state kueue.CheckState) bool {
	if checkState.State == state {
		return false
	}
	checkState.State = state
	return true
}

func (wlInfo *workloadInfo) update(wl *kueue.Workload, c clock.Clock) {
	for _, check := range wl.Status.AdmissionChecks {
		workload.SetAdmissionCheckState(&wlInfo.checkStates, check, c)
	}
	wlInfo.requeueState = wl.Status.RequeueState
}

func (c *Controller) syncCheckStates(
	ctx context.Context, wl *kueue.Workload,
	wlInfo *workloadInfo,
	checkConfig map[kueue.AdmissionCheckReference]*kueue.ProvisioningRequestConfig,
	activeOrLastPRForChecks map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest,
) error {
	log := ctrl.LoggerFrom(ctx)
	wlInfo.update(wl, c.clock)
	checksMap := slices.ToRefMap(wl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) kueue.AdmissionCheckReference { return c.Name })
	wlPatch := workload.BaseSSAWorkload(wl, true)
	wlPatch.Status.RequeueState = wl.Status.RequeueState.DeepCopy()
	recorderMessages := make([]string, 0, len(checkConfig))
	updated := false
	for check, prc := range checkConfig {
		checkState := *checksMap[check]
		//nolint:gocritic // ignore ifElseChain
		if prc == nil {
			// the check is not active
			updated = updateCheckState(&checkState, kueue.CheckStatePending) || updated
			updated = updateCheckMessage(&checkState, CheckInactiveMessage) || updated
		} else if !c.reqIsNeeded(wl, prc) {
			if updateCheckState(&checkState, kueue.CheckStateReady) {
				updated = true
				checkState.Message = NoRequestNeeded
				checkState.PodSetUpdates = nil
			}
		} else {
			pr := activeOrLastPRForChecks[check]
			if pr == nil {
				return nil
			}
			log.V(3).Info("Synchronizing admission check state based on provisioning request", "wl", klog.KObj(wl),
				"check", check,
				"prName", pr.Name,
				"failed", isFailed(pr),
				"provisioned", isProvisioned(pr),
				"accepted", isAccepted(pr),
				"bookingExpired", isBookingExpired(pr),
				"capacityRevoked", isCapacityRevoked(pr))
			backoffBaseSeconds := *prc.Spec.RetryStrategy.BackoffBaseSeconds
			backoffMaxSeconds := *prc.Spec.RetryStrategy.BackoffMaxSeconds
			backoffLimitCount := *prc.Spec.RetryStrategy.BackoffLimitCount
			switch {
			case isFailed(pr):
				if attempt := getAttempt(log, pr, wl.Name, check); attempt <= backoffLimitCount {
					// it is going to be retried
					message := fmt.Sprintf("Retrying after failure: %s", apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Failed).Message)
					updated = updateCheckMessage(&checkState, message) || updated
					if wl.Status.RequeueState == nil || getAttempt(log, pr, wl.Name, check) > ptr.Deref(wl.Status.RequeueState.Count, 0) {
						// We don't want to Retry on old ProvisioningRequests
						updated = true
						updateCheckState(&checkState, kueue.CheckStateRetry)
						workload.UpdateRequeueState(wlPatch, backoffBaseSeconds, backoffMaxSeconds, c.clock)
					}
				} else {
					updated = true
					checkState.State = kueue.CheckStateRejected
					checkState.Message = apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Failed).Message
				}
			case isCapacityRevoked(pr):
				if workload.IsActive(wl) && !workload.IsFinished(wl) {
					// We mark the admission check as rejected to trigger workload deactivation.
					// This is needed to prevent replacement pods being stuck in the pending phase indefinitely
					// as the nodes are already deleted by Cluster Autoscaler.
					updated = updateCheckState(&checkState, kueue.CheckStateRejected) || updated
					updated = updateCheckMessage(&checkState, apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.CapacityRevoked).Message) || updated
				}
			case isBookingExpired(pr):
				if !workload.IsAdmitted(wl) {
					if attempt := getAttempt(log, pr, wl.Name, check); attempt <= backoffLimitCount {
						// it is going to be retried
						message := fmt.Sprintf("Retrying after booking expired: %s", apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.BookingExpired).Message)
						updated = updateCheckMessage(&checkState, message) || updated
						if wl.Status.RequeueState == nil || getAttempt(log, pr, wl.Name, check) > ptr.Deref(wl.Status.RequeueState.Count, 0) {
							updated = true
							updateCheckState(&checkState, kueue.CheckStateRetry)
							workload.UpdateRequeueState(wlPatch, backoffBaseSeconds, backoffMaxSeconds, c.clock)
						}
					} else {
						updated = true
						checkState.State = kueue.CheckStateRejected
						checkState.Message = apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.BookingExpired).Message
					}
				}
			case isProvisioned(pr):
				if updateCheckState(&checkState, kueue.CheckStateReady) {
					updated = true
					// add the pod podSetUpdates
					checkState.PodSetUpdates = podSetUpdates(log, wl, pr, prc)
					// propagate the message from the provisioning request status into the workload
					// to change to the "successfully provisioned" message after provisioning
					updateCheckMessage(&checkState, apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Provisioned).Message)
				}
			case isAccepted(pr):
				if provisionedCond := apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Provisioned); provisionedCond != nil {
					// propagate the ETA update from the provisioning request into the workload
					updated = updateCheckMessage(&checkState, provisionedCond.Message) || updated
					updated = updateCheckState(&checkState, kueue.CheckStatePending) || updated
				}
			default:
				updated = updateCheckState(&checkState, kueue.CheckStatePending) || updated
			}
		}

		existingCondition := workload.FindAdmissionCheck(wlPatch.Status.AdmissionChecks, checkState.Name)
		if existingCondition != nil && existingCondition.State != checkState.State {
			message := fmt.Sprintf("Admission check %s updated state from %s to %s", checkState.Name, existingCondition.State, checkState.State)
			if checkState.Message != "" {
				message += fmt.Sprintf(" with message %s", checkState.Message)
			}
			recorderMessages = append(recorderMessages, message)
		}
		workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, checkState, c.clock)
	}
	if updated {
		if err := c.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(kueue.ProvisioningRequestControllerName), client.ForceOwnership); err != nil {
			return err
		}
		for i := range recorderMessages {
			c.record.Event(wl, corev1.EventTypeNormal, "AdmissionCheckUpdated", api.TruncateEventMessage(recorderMessages[i]))
		}
	}
	wlInfo.update(wlPatch, c.clock)
	return nil
}

func podSetUpdates(log logr.Logger, wl *kueue.Workload, pr *autoscaling.ProvisioningRequest, prc *kueue.ProvisioningRequestConfig) []kueue.PodSetUpdate {
	podSets := wl.Spec.PodSets
	refMap := slices.ToMap(podSets, func(i int) (string, kueue.PodSetReference) {
		return getProvisioningRequestPodTemplateName(pr.Name, podSets[i].Name), podSets[i].Name
	})
	return slices.Map(pr.Spec.PodSets, func(ps *autoscaling.PodSet) kueue.PodSetUpdate {
		podSetUpdate := kueue.PodSetUpdate{
			Name: refMap[ps.PodTemplateRef.Name],
			Annotations: map[string]string{
				ConsumesAnnotationKey:  pr.Name,
				ClassNameAnnotationKey: pr.Spec.ProvisioningClassName},
		}
		if psUpdate := prc.Spec.PodSetUpdates; psUpdate != nil {
			podSetUpdate.NodeSelector = make(map[string]string, len(psUpdate.NodeSelector))
			for _, nodeSelector := range psUpdate.NodeSelector {
				value, ok := pr.Status.ProvisioningClassDetails[nodeSelector.ValueFromProvisioningClassDetail]
				if !ok {
					log.Info("skipping detail not found in the ProvisioningClassDetails", "detail", nodeSelector.ValueFromProvisioningClassDetail)
					continue
				}
				podSetUpdate.NodeSelector[nodeSelector.Key] = string(value)
			}
		}
		return podSetUpdate
	})
}

type acHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*acHandler)(nil)

func (a *acHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	ac, isAc := event.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return
	}

	if ac.Spec.ControllerName == kueue.ProvisioningRequestControllerName {
		err := a.reconcileWorkloadsUsing(ctx, ac.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "admissionCheck", klog.KObj(ac))
		}
	}
}

func (a *acHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldAc, isOldAc := event.ObjectOld.(*kueue.AdmissionCheck)
	newAc, isNewAc := event.ObjectNew.(*kueue.AdmissionCheck)
	if !isNewAc || !isOldAc {
		return
	}

	if oldAc.Spec.ControllerName == kueue.ProvisioningRequestControllerName || newAc.Spec.ControllerName == kueue.ProvisioningRequestControllerName {
		err := a.reconcileWorkloadsUsing(ctx, oldAc.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "admissionCheck", klog.KObj(oldAc))
		}
	}
}

func (a *acHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	ac, isAc := event.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return
	}

	if ac.Spec.ControllerName == kueue.ProvisioningRequestControllerName {
		err := a.reconcileWorkloadsUsing(ctx, ac.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "admissionCheck", klog.KObj(ac))
		}
	}
}

func (a *acHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// nothing to do for now
}

func (a *acHandler) reconcileWorkloadsUsing(ctx context.Context, check string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	wls := &kueue.WorkloadList{}
	if err := a.client.List(ctx, wls, client.MatchingFields{WorkloadsWithAdmissionCheckKey: check}); client.IgnoreNotFound(err) != nil {
		return err
	}

	for i := range wls.Items {
		wl := &wls.Items[i]
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      wl.Name,
				Namespace: wl.Namespace,
			},
		}
		q.Add(req)
	}

	return nil
}

type prcHandler struct {
	client            client.Client
	acHandlerOverride func(ctx context.Context, config string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error
}

var _ handler.EventHandler = (*prcHandler)(nil)

func (p *prcHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	prc, isPRC := event.Object.(*kueue.ProvisioningRequestConfig)
	if !isPRC {
		return
	}
	err := p.reconcileWorkloadsUsing(ctx, prc.Name, q)
	if err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "provisioningRequestConfig", klog.KObj(prc))
	}
}

func (p *prcHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldPRC, isOldPRC := event.ObjectOld.(*kueue.ProvisioningRequestConfig)
	newPRC, isNewPRC := event.ObjectNew.(*kueue.ProvisioningRequestConfig)
	if !isNewPRC || !isOldPRC {
		return
	}

	if oldPRC.Spec.ProvisioningClassName != newPRC.Spec.ProvisioningClassName || !maps.Equal(oldPRC.Spec.Parameters, newPRC.Spec.Parameters) || !slices.CmpNoOrder(oldPRC.Spec.ManagedResources, newPRC.Spec.ManagedResources) {
		err := p.reconcileWorkloadsUsing(ctx, oldPRC.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "provisioningRequestConfig", klog.KObj(oldPRC))
		}
	}
}

func (p *prcHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	prc, isPRC := event.Object.(*kueue.ProvisioningRequestConfig)
	if !isPRC {
		return
	}
	err := p.reconcileWorkloadsUsing(ctx, prc.Name, q)
	if err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "provisioningRequestConfig", klog.KObj(prc))
	}
}

func (p *prcHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// nothing to do for now
}

func (p *prcHandler) reconcileWorkloadsUsing(ctx context.Context, config string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	acs := &kueue.AdmissionCheckList{}
	if err := p.client.List(ctx, acs, client.MatchingFields{AdmissionCheckUsingConfigKey: config}); client.IgnoreNotFound(err) != nil {
		return err
	}
	users := slices.Map(acs.Items, func(ac *kueue.AdmissionCheck) string { return ac.Name })
	for _, user := range users {
		if p.acHandlerOverride != nil {
			if err := p.acHandlerOverride(ctx, user, q); err != nil {
				return err
			}
		} else {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: user,
				},
			}
			q.Add(req)
		}
	}
	return nil
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	ach := &acHandler{
		client: c.client,
	}
	prch := &prcHandler{
		client:            c.client,
		acHandlerOverride: ach.reconcileWorkloadsUsing,
	}
	err := ctrl.NewControllerManagedBy(mgr).
		Named("provisioning_workload").
		For(&kueue.Workload{}).
		Owns(&autoscaling.ProvisioningRequest{}).
		Watches(&kueue.AdmissionCheck{}, ach).
		Watches(&kueue.ProvisioningRequestConfig{}, prch).
		Complete(c)
	if err != nil {
		return err
	}

	prcACh := &prcHandler{
		client: c.client,
	}
	acReconciler := &acReconciler{
		client: c.client,
		helper: c.helper,
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("provisioning_admissioncheck").
		For(&kueue.AdmissionCheck{}).
		Watches(&kueue.ProvisioningRequestConfig{}, prcACh).
		Complete(acReconciler)
}

func limitObjectName(fullName string) string {
	if len(fullName) <= objNameMaxPrefixLength {
		return fullName
	}
	h := sha1.New()
	h.Write([]byte(fullName))
	hashBytes := hex.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("%s-%s", fullName[:objNameMaxPrefixLength], hashBytes[:objNameHashLength])
}

type MergedPodSet struct {
	Name             kueue.PodSetReference
	PodSet           *kueue.PodSet
	PodSetAssignment *kueue.PodSetAssignment
	Count            int32
}

func mergePodSets(
	wl *kueue.Workload,
	prcSpec *kueue.ProvisioningRequestConfigSpec,
) ([]MergedPodSet, error) {
	expectedPodSets := requiredPodSets(wl.Spec.PodSets, prcSpec.ManagedResources)
	psaMap := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(p *kueue.PodSetAssignment) kueue.PodSetReference { return p.Name })
	podSetMap := slices.ToRefMap(wl.Spec.PodSets, func(ps *kueue.PodSet) kueue.PodSetReference { return ps.Name })

	mergePolicy := prcSpec.PodSetMergePolicy
	mergedPodSets := []MergedPodSet{}
	for _, psName := range expectedPodSets {
		ps, psFound := podSetMap[psName]
		psa, psaFound := psaMap[psName]
		if !psFound || !psaFound {
			return nil, errInconsistentPodSetAssignments
		}

		merged := false
		if mergePolicy != nil {
			for i, mps := range mergedPodSets {
				if merged = canMergePodSets(mps.PodSet, ps, mergePolicy); merged {
					mergedPodSets[i].Count += ptr.Deref(psa.Count, ps.Count)
					break
				}
			}
		}

		if !merged {
			mergedPodSets = append(mergedPodSets, MergedPodSet{
				Name:             psName,
				PodSet:           ps,
				PodSetAssignment: psa,
				Count:            ptr.Deref(psa.Count, ps.Count),
			})
		}
	}

	return mergedPodSets, nil
}

func canMergePodSets(ps1, ps2 *kueue.PodSet, mergePolicy *kueue.ProvisioningRequestConfigPodSetMergePolicy) bool {
	switch *mergePolicy {
	case kueue.IdenticalPodTemplates:
		return equality.Semantic.DeepEqual(ps1.Template, ps2.Template)
	case kueue.IdenticalWorkloadSchedulingRequirements:
		return arePodSetsSimilar(ps1, ps2)
	default:
		return false
	}
}

func arePodSetsSimilar(ps1, ps2 *kueue.PodSet) bool {
	return areContainersEqual(ps1.Template.Spec.Containers, ps2.Template.Spec.Containers) &&
		areContainersEqual(ps1.Template.Spec.InitContainers, ps2.Template.Spec.InitContainers) &&
		equality.Semantic.DeepEqual(ps1.Template.Spec.Resources, ps2.Template.Spec.Resources) &&
		equality.Semantic.DeepEqual(ps1.Template.Spec.NodeSelector, ps2.Template.Spec.NodeSelector) &&
		equality.Semantic.DeepEqual(ps1.Template.Spec.Tolerations, ps2.Template.Spec.Tolerations) &&
		equality.Semantic.DeepEqual(ps1.Template.Spec.Affinity, ps2.Template.Spec.Affinity) &&
		equality.Semantic.DeepEqual(ps1.Template.Spec.ResourceClaims, ps2.Template.Spec.ResourceClaims)
}

// areContainersEqual compares the resource requests of containers between two lists of PodSet containers.
func areContainersEqual(ps1Containers []corev1.Container, ps2Containers []corev1.Container) bool {
	if len(ps1Containers) != len(ps2Containers) {
		return false
	}
	for i := range ps1Containers {
		if !equality.Semantic.DeepEqual(ps1Containers[i].Resources.Requests, ps2Containers[i].Resources.Requests) {
			return false
		}
	}
	return true
}
