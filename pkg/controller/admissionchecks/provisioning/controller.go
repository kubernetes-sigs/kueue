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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
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
	client      client.Client
	record      events.EventRecorder
	helper      *provisioningConfigHelper
	clock       clock.Clock
	roleTracker *roletracker.RoleTracker
}

type workloadInfo struct {
	checkStates []kueue.AdmissionCheckState
}

var _ reconcile.Reconciler = (*Controller)(nil)

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=podtemplates,verbs=get;list;watch;create;delete;update
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=provisioningrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=provisioningrequests/status,verbs=get
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=admissionchecks,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=provisioningrequestconfigs,verbs=get;list;watch

func NewController(client client.Client, record events.EventRecorder, roleTracker *roletracker.RoleTracker) (*Controller, error) {
	helper, err := newProvisioningConfigHelper(client)
	if err != nil {
		return nil, err
	}
	return &Controller{
		client:      client,
		record:      record,
		helper:      helper,
		clock:       realClock,
		roleTracker: roleTracker,
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

	isFinished := workloadfinish.IsFinished(wl)
	isEvicted := workloadevict.IsEvicted(wl)
	if !workload.HasQuotaReservation(wl) && !isFinished && !isEvicted {
		return reconcile.Result{}, nil
	}

	provReqs := &autoscaling.ProvisioningRequestList{}
	if err := c.client.List(ctx, provReqs, client.InNamespace(wl.Namespace), client.MatchingFields{RequestsOwnedByWorkloadKey: wl.Name}); client.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, err
	}

	if isFinished || (isEvicted && features.Enabled(features.CleanupProvisioningRequestsOnEviction)) {
		err = c.deleteUnusedProvisioningRequests(ctx, provReqs.Items, nil)
		if err != nil {
			log.V(2).Error(err, "failed to delete stale provisioning requests")
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}
	if isEvicted {
		return reconcile.Result{}, nil
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

func (c *Controller) deleteUnusedProvisioningRequests(
	ctx context.Context,
	ownedPRs []autoscaling.ProvisioningRequest,
	activeOrLastPRForChecks map[kueue.AdmissionCheckReference]*autoscaling.ProvisioningRequest,
) error {
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
		ac := admissioncheck.FindAdmissionCheck(wlInfo.checkStates, checkName)
		if ac != nil && ac.State != kueue.CheckStatePending {
			// Skip non-Pending checks (Ready done; Retry/Rejected wait for eviction).
			log.V(2).Info("Skip syncing of the ProvReq for admission check which is not Pending", "workload", klog.KObj(wl), "admissionCheck", checkName, "state", ac.State)
			continue
		}

		req, exists := activeOrLastPRForChecks[checkName]
		attempt := int32(1)
		if ac != nil {
			// When cleanup deleted the previous ProvisioningRequest, the Workload
			// admission check keeps the retry count needed to name the next attempt.
			attempt = ptr.Deref(ac.RetryCount, 0) + 1
		}
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

				desired, err := c.buildPodTemplate(ctx, wl, ptName, mergedPodSet.PodSet, mergedPodSet.PodSetAssignment)
				if err != nil {
					msg := fmt.Sprintf("Error building PodTemplate %q: %v", ptName, err)
					return c.handleError(ctx, wl, ac, msg, err)
				}

				existing := &corev1.PodTemplate{}
				err = c.client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
				if client.IgnoreNotFound(err) != nil {
					msg := fmt.Sprintf("Error getting PodTemplate %q: %v", ptName, err)
					return c.handleError(ctx, wl, ac, msg, err)
				}
				switch {
				case err != nil:
					// it's a not found, so create it
					if err := c.client.Create(ctx, desired); err != nil {
						msg := fmt.Sprintf("Error creating PodTemplate %q: %v", ptName, err)
						return c.handleError(ctx, wl, ac, msg, err)
					}
					log.V(3).Info("Created PodTemplate", "podTemplate", klog.KObj(desired))
				case podTemplateEquivalent(existing, desired):
					// Already matches the Kueue-derived spec; skip the write.
					log.V(3).Info("PodTemplate already up to date, skipping update", "podTemplate", klog.KObj(desired))
				default:
					// Divergent PodTemplate at the deterministic name. Replace it with
					// Kueue-derived contents so the ProvisioningRequest never adopts
					// foreign/stale specs. Do not Retry the admission check: leftover
					// templates from a prior admission (e.g. after preemption onto another
					// flavor) share this name, and Retry would block re-admission.
					if features.Enabled(features.RetryProvisioningDueInconsistentPodTemplate) {
						if err := c.client.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
							msg := fmt.Sprintf("Error deleting divergent PodTemplate %q: %v", ptName, err)
							return c.handleError(ctx, wl, ac, msg, err)
						}
						if err := c.client.Create(ctx, desired); err != nil {
							// Delete may still be finalizing; Retry with a new attempt/name.
							if apierrors.IsAlreadyExists(err) {
								msg := fmt.Sprintf("Existing PodTemplate %q differs from the expected Kueue-derived contents; retrying", ptName)
								return c.handleRetry(ctx, log, wl, ac, prc, msg)
							}
							msg := fmt.Sprintf("Error recreating PodTemplate %q: %v", ptName, err)
							return c.handleError(ctx, wl, ac, msg, err)
						}
						log.V(3).Info("Replaced divergent PodTemplate", "podTemplate", klog.KObj(desired))
					} else {
						log.V(3).Info("PodTemplate differs but RetryProvisioningDueInconsistentPodTemplate is disabled; reusing existing", "podTemplate", klog.KObj(existing))
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
			c.record.Eventf(wl, nil, corev1.EventTypeNormal, "ProvisioningRequestCreated", "Created", "Created ProvisioningRequest: %q", req.Name)
			activeOrLastPRForChecks[checkName] = req
		}
		if err := c.syncProvisionRequestsPodTemplates(ctx, wl, req); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) handleError(ctx context.Context, wl *kueue.Workload, ac *kueue.AdmissionCheckState, msg string, err error) error {
	c.record.Eventf(wl, nil, corev1.EventTypeWarning, "FailedCreate", "FailedCreate", api.TruncateEventMessage(msg))
	patchErr := workloadpatching.PatchStatus(ctx, c.client, wl, kueue.ProvisioningRequestControllerName, func(wl *kueue.Workload) (bool, error) {
		ac.Message = api.TruncateConditionMessage(msg)
		ac.LastTransitionTime = metav1.NewTime(c.clock.Now())
		return workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, *ac, c.clock), nil
	})
	return errors.Join(err, patchErr)
}

// buildPodTemplate derives a PodTemplate from the Workload PodSet and admission assignment.
func (c *Controller) buildPodTemplate(ctx context.Context, wl *kueue.Workload, name string, ps *kueue.PodSet, psa *kueue.PodSetAssignment) (*corev1.PodTemplate, error) {
	newPt := &corev1.PodTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: wl.Namespace,
			Labels: map[string]string{
				constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
			},
		},
		// Deep-copy: podset.Merge mutates in place and ps.Template aliases wl.Spec.PodSets.
		Template: *ps.Template.DeepCopy(),
	}

	// set the controller reference to workload so that the template is not left orphaned
	// if the ProvisioningRequest creation fails. The ownership is later transferred to the
	// ProvisioningRequest.
	if err := ctrl.SetControllerReference(wl, newPt, c.client.Scheme()); err != nil {
		return nil, err
	}

	// apply the admission node selectors to the Template
	psi, err := podset.FromAssignment(ctx, c.client, psa, ps)
	if err != nil {
		return nil, err
	}

	err = podset.Merge(ctrl.LoggerFrom(ctx), &newPt.Template.ObjectMeta, &newPt.Template.Spec, psi)
	if err != nil {
		return nil, err
	}

	// copy limits to requests if needed
	workload.UseLimitsAsMissingRequestsInPod(&newPt.Template.Spec)

	return newPt, nil
}

// handleRetry sets the admission check to Retry so the next attempt uses a new name pair.
func (c *Controller) handleRetry(ctx context.Context, log logr.Logger, wl *kueue.Workload, ac *kueue.AdmissionCheckState, prc *kueue.ProvisioningRequestConfig, msg string) error {
	prevState := ac.State
	ac.Message = api.TruncateConditionMessage(msg)
	setAdmissionCheckRetry(ac, prc, c.clock)

	err := workloadpatching.PatchStatus(ctx, c.client, wl, kueue.ProvisioningRequestControllerName, func(wl *kueue.Workload) (bool, error) {
		return workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, *ac, c.clock), nil
	})
	if err != nil {
		return err
	}
	eventMsg := fmt.Sprintf("Admission check %s updated state from %s to %s with message: %s", ac.Name, prevState, ac.State, ac.Message)
	c.record.Eventf(wl, nil, corev1.EventTypeNormal, "UpdatedAdmissionCheck", "UpdatedAdmissionCheck", api.TruncateEventMessage(eventMsg))
	log.V(3).Info("Retrying admission check due to divergent PodTemplate", "admissionCheck", ac.Name, "message", msg)
	return nil
}

// setAdmissionCheckRetry sets the admission check to Retry using the
// ProvisioningRequestConfig retry strategy (backoff + requeue state).
func setAdmissionCheckRetry(ac *kueue.AdmissionCheckState, prc *kueue.ProvisioningRequestConfig, clk clock.Clock) {
	ac.State = kueue.CheckStateRetry
	workload.UpdateAdmissionCheckRequeueState(ac,
		*prc.Spec.RetryStrategy.BackoffBaseSeconds,
		*prc.Spec.RetryStrategy.BackoffMaxSeconds,
		clk)
}

// podTemplateEquivalent compares Template and object Labels. Owners are ignored
// (transferred later). Labels are compared as-is after empty-map normalization,
// so label hijacking still fails equivalence.
func podTemplateEquivalent(existing, desired *corev1.PodTemplate) bool {
	a := existing.DeepCopy()
	b := desired.DeepCopy()
	normalizePodTemplateForCompare(a)
	normalizePodTemplateForCompare(b)
	return equality.Semantic.DeepEqual(a.Template, b.Template) &&
		equality.Semantic.DeepEqual(a.Labels, b.Labels)
}

// normalizePodTemplateForCompare strips API-server defaults that differ from
// in-memory builds (RestartPolicy, DNSPolicy, etc.) so re-reconcile does not
// falsely treat an unchanged PodTemplate as divergent. Empty label/annotation
// maps are nilled only for empty-vs-nil DeepEqual; non-empty labels are kept.
func normalizePodTemplateForCompare(pt *corev1.PodTemplate) {
	meta := &pt.Template.ObjectMeta
	if len(meta.Labels) == 0 {
		meta.Labels = nil
	}
	if len(meta.Annotations) == 0 {
		meta.Annotations = nil
	}

	spec := &pt.Template.Spec
	spec.RestartPolicy = ""
	spec.DNSPolicy = ""
	spec.SchedulerName = ""
	spec.TerminationGracePeriodSeconds = nil
	// API server defaults an empty PodSecurityContext; in-memory builds leave it nil.
	if spec.SecurityContext != nil && equality.Semantic.DeepEqual(spec.SecurityContext, &corev1.PodSecurityContext{}) {
		spec.SecurityContext = nil
	}
	for i := range spec.Containers {
		normalizeContainerForCompare(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		normalizeContainerForCompare(&spec.InitContainers[i])
	}
}

func normalizeContainerForCompare(c *corev1.Container) {
	c.ImagePullPolicy = ""
	c.TerminationMessagePath = ""
	c.TerminationMessagePolicy = ""
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
		workloadpatching.SetAdmissionCheckState(&wlInfo.checkStates, check, c)
	}
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
	recorderMessages := make([]string, 0, len(checkConfig))
	updated := false
	err := workloadpatching.PatchStatus(ctx, c.client, wl, kueue.ProvisioningRequestControllerName, func(wlPatch *kueue.Workload) (bool, error) {
		// Inside PatchStatus (Apply), we use BaseSSAWorkload() to create the patch.
		// This patch does not include wl.Status.AdmissionChecks, which leads to errors
		// in subsequent steps due to the missing field.
		// We should deep-copy the admission checks into wlPatch.
		// NOTE: Once WorkloadRequestUseMergePatch reaches GA, this deep copy can be removed.
		wlPatch.Status.AdmissionChecks = make([]kueue.AdmissionCheckState, len(wl.Status.AdmissionChecks))
		for index := range wl.Status.AdmissionChecks {
			wlPatch.Status.AdmissionChecks[index] = *wl.Status.AdmissionChecks[index].DeepCopy()
		}

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
					return false, nil
				}
				log.V(3).Info("Synchronizing admission check state based on provisioning request", "wl", klog.KObj(wl),
					"check", check,
					"prName", pr.Name,
					"failed", isFailed(pr),
					"provisioned", isProvisioned(pr),
					"accepted", isAccepted(pr),
					"bookingExpired", isBookingExpired(pr),
					"capacityRevoked", isCapacityRevoked(pr))
				backoffLimitCount := *prc.Spec.RetryStrategy.BackoffLimitCount
				switch {
				case isFailed(pr):
					if attempt := getAttempt(log, pr, wl.Name, check); attempt <= backoffLimitCount {
						// it is going to be retried
						message := fmt.Sprintf("Retrying after failure: %s", apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Failed).Message)
						updated = updateCheckMessage(&checkState, message) || updated
						if getAttempt(log, pr, wl.Name, check) > ptr.Deref(checkState.RetryCount, 0) {
							// We don't want to Retry on old ProvisioningRequests
							updated = true
							setAdmissionCheckRetry(&checkState, prc, c.clock)
						}
					} else {
						updated = true
						checkState.State = kueue.CheckStateRejected
						checkState.Message = apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Failed).Message
					}
				case isCapacityRevoked(pr):
					if workload.IsActive(wl) && !workloadfinish.IsFinished(wl) {
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
							if getAttempt(log, pr, wl.Name, check) > ptr.Deref(checkState.RetryCount, 0) {
								updated = true
								setAdmissionCheckRetry(&checkState, prc, c.clock)
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

			existingCondition := admissioncheck.FindAdmissionCheck(wlPatch.Status.AdmissionChecks, checkState.Name)
			if existingCondition != nil && existingCondition.State != checkState.State {
				message := fmt.Sprintf("Admission check %s updated state from %s to %s", checkState.Name, existingCondition.State, checkState.State)
				if checkState.Message != "" {
					message += fmt.Sprintf(" with message: %s", checkState.Message)
				}
				recorderMessages = append(recorderMessages, message)
			}
			workloadpatching.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, checkState, c.clock)
		}
		return updated, nil
	})
	if err != nil {
		return err
	}
	if updated {
		for i := range recorderMessages {
			c.record.Eventf(wl, nil, corev1.EventTypeNormal, "UpdatedAdmissionCheck", "UpdatedAdmissionCheck", api.TruncateEventMessage(recorderMessages[i]))
		}
	}
	wlInfo.update(wl, c.clock)
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
				autoscaling.ProvisioningRequestPodAnnotationKey: pr.Name,
				autoscaling.ProvisioningClassPodAnnotationKey:   pr.Spec.ProvisioningClassName,
			},
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

	if oldPRC.Spec.ProvisioningClassName != newPRC.Spec.ProvisioningClassName || !maps.Equal(oldPRC.Spec.Parameters, newPRC.Spec.Parameters) ||
		!slices.CmpNoOrder(oldPRC.Spec.ManagedResources, newPRC.Spec.ManagedResources) {
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
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(c.roleTracker, "provisioning-workload"),
		}).
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
		WithOptions(controller.Options{
			LogConstructor: roletracker.NewLogConstructor(c.roleTracker, "provisioning-admissioncheck"),
		}).
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
