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

package provisioning

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/podset"
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

type Controller struct {
	client client.Client
	helper *storeHelper
	record record.EventRecorder
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

func NewController(client client.Client, record record.EventRecorder) *Controller {
	return &Controller{
		client: client,
		record: record,
		helper: &storeHelper{
			client: client,
		},
	}
}

// Reconcile performs a full reconciliation for the object referred to by the Request.
// The Controller will requeue the Request to be processed again if an error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	wl := &kueue.Workload{}
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Reconcile workload")

	err := c.client.Get(ctx, req.NamespacedName, wl)
	if err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !workload.HasQuotaReservation(wl) || apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
		//1.2 workload has no reservation or is finished
		log.V(5).Info("workload with no reservation, delete owned requests")
		return reconcile.Result{}, c.deleteOwnedProvisionRequests(ctx, req.Namespace, req.Name)
	}

	// get the lists of relevant checks
	relevantChecks, err := c.helper.FilterChecksForProvReq(ctx, wl.Status.AdmissionChecks)
	if err != nil {
		return reconcile.Result{}, err
	}

	if workload.IsAdmitted(wl) {
		// check the state of the provision requests, eventually toggle the checks to false
		// otherwise there is nothing to here
		log.V(5).Info("workload admitted, sync checks")
		return reconcile.Result{}, c.syncCheckStates(ctx, wl, relevantChecks)
	}

	if err := c.syncOwnedProvisionRequest(ctx, wl, relevantChecks); err != nil {
		// this can also delete unneeded checks
		log.V(2).Error(err, "syncOwnedProvisionRequest failed")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, c.syncCheckStates(ctx, wl, relevantChecks)
}

func (c *Controller) deleteOwnedProvisionRequests(ctx context.Context, namespace string, name string) error {
	list := &autoscaling.ProvisioningRequestList{}
	if err := c.client.List(ctx, list, client.InNamespace(namespace), client.MatchingFields{RequestsOwnedByWorkloadKey: name}); err != nil {
		return client.IgnoreNotFound(err)
	}

	for i := range list.Items {
		if err := c.client.Delete(ctx, &list.Items[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete requests for %s/%s: %w", namespace, name, err)
		}
	}
	return nil
}

func (c *Controller) syncOwnedProvisionRequest(ctx context.Context, wl *kueue.Workload, relevantChecks []string) error {
	log := ctrl.LoggerFrom(ctx)
	list := &autoscaling.ProvisioningRequestList{}
	if err := c.client.List(ctx, list, client.InNamespace(wl.Namespace), client.MatchingFields{RequestsOwnedByWorkloadKey: wl.Name}); client.IgnoreNotFound(err) != nil {
		return err
	}

	requestExists := slices.ToMap(relevantChecks, func(i int) (string, bool) { return GetProvisioningRequestName(wl.Name, relevantChecks[i]), false })
	requestNeededForCheck := slices.ToMap(relevantChecks, func(i int) (string, bool) { return relevantChecks[i], c.requestIsNeeded(ctx, wl, relevantChecks[i]) })
	requestToCheckName := slices.ToMap(relevantChecks, func(i int) (string, string) {
		return GetProvisioningRequestName(wl.Name, relevantChecks[i]), relevantChecks[i]
	})

	for i := range list.Items {
		req := &list.Items[i]
		if _, needed := requestExists[req.Name]; !needed {
			if err := c.client.Delete(ctx, req); client.IgnoreNotFound(err) != nil {
				return err
			}
		} else {
			// get the parameters
			checkName := requestToCheckName[req.Name]
			prc, err := c.helper.ProvReqConfigForAdmissionCheck(ctx, checkName)
			if err != nil || !requestNeededForCheck[checkName] || !requestHasParamaters(req, prc) {
				// the check is not active or the parameters are out of sync
				if err := c.client.Delete(ctx, req); client.IgnoreNotFound(err) != nil {
					log.V(5).Error(err, "deleting the request", "check", checkName)
					return err
				}
			} else {
				requestExists[req.Name] = true
			}
		}
	}

	// create the missing ones
	for requestName, exists := range requestExists {
		checkName := requestToCheckName[requestName]
		if !requestNeededForCheck[checkName] {
			continue
		}
		//get the config
		prc, err := c.helper.ProvReqConfigForAdmissionCheck(ctx, checkName)
		if err != nil {
			// the check is not active
			continue
		}
		if !exists {
			req := &autoscaling.ProvisioningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      requestName,
					Namespace: wl.Namespace,
				},
				Spec: autoscaling.ProvisioningRequestSpec{
					ProvisioningClassName: prc.Spec.ProvisioningClassName,
					Parameters:            parametersKueueToProvisioning(prc.Spec.Parameters),
				},
			}

			expectedPodSets := requiredPodSets(wl.Spec.PodSets, prc.Spec.ManagedResources)
			psaMap := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(p *kueue.PodSetAssignment) string { return p.Name })
			podSetMap := slices.ToRefMap(wl.Spec.PodSets, func(ps *kueue.PodSet) string { return ps.Name })
			for _, psName := range expectedPodSets {
				ps, psFound := podSetMap[psName]
				psa, psaFound := psaMap[psName]
				if !psFound || !psaFound {
					return errInconsistentPodSetAssignments
				}
				req.Spec.PodSets = append(req.Spec.PodSets, autoscaling.PodSet{
					PodTemplateRef: autoscaling.Reference{
						Name: getProvisioningRequestPodTemplateName(wl.Name, checkName, psName),
					},
					Count: ptr.Deref(psa.Count, ps.Count),
				})
			}

			if err := ctrl.SetControllerReference(wl, req, c.client.Scheme()); err != nil {
				return err
			}

			if err := c.client.Create(ctx, req); err != nil {
				return err
			}
		}
		if err := c.syncProvisionRequestsPodTemplates(ctx, wl, checkName, prc); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) syncProvisionRequestsPodTemplates(ctx context.Context, wl *kueue.Workload, checkName string, prc *kueue.ProvisioningRequestConfig) error {
	request := &autoscaling.ProvisioningRequest{}
	requestKey := types.NamespacedName{
		Name:      GetProvisioningRequestName(wl.Name, checkName),
		Namespace: wl.Namespace,
	}
	err := c.client.Get(ctx, requestKey, request)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	expectedPodSets := requiredPodSets(wl.Spec.PodSets, prc.Spec.ManagedResources)
	podsetRefsMap := slices.ToMap(expectedPodSets, func(i int) (string, string) {
		return getProvisioningRequestPodTemplateName(wl.Name, checkName, expectedPodSets[i]), expectedPodSets[i]
	})

	// the order of the podSets should be the same in the workload and prov. req.
	// if the number is different, just delete the request
	if len(request.Spec.PodSets) != len(expectedPodSets) {
		return c.client.Delete(ctx, request)
	}

	psaMap := slices.ToRefMap(wl.Status.Admission.PodSetAssignments, func(p *kueue.PodSetAssignment) string { return p.Name })
	podSetMap := slices.ToRefMap(wl.Spec.PodSets, func(ps *kueue.PodSet) string { return ps.Name })

	for i := range request.Spec.PodSets {
		reqPS := &request.Spec.PodSets[i]
		psName, refFound := podsetRefsMap[reqPS.PodTemplateRef.Name]
		ps, psFound := podSetMap[psName]
		psa, psaFound := psaMap[psName]

		if !refFound || !psFound || !psaFound || ptr.Deref(psa.Count, 0) != reqPS.Count {
			return c.client.Delete(ctx, request)
		}

		pt := &corev1.PodTemplate{}
		ptKey := types.NamespacedName{
			Namespace: request.Namespace,
			Name:      reqPS.PodTemplateRef.Name,
		}

		err := c.client.Get(ctx, ptKey, pt)

		if client.IgnoreNotFound(err) != nil {
			return err
		}

		if err != nil {
			// it's a not found, so create it
			newPt := &corev1.PodTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ptKey.Name,
					Namespace: ptKey.Namespace,
				},
				Template: ps.Template,
			}

			// apply the admission node selectors to the Template
			psi, err := podset.FromAssignment(ctx, c.client, psaMap[psName], reqPS.Count)
			if err != nil {
				return err
			}

			err = podset.Merge(&newPt.Template.ObjectMeta, &newPt.Template.Spec, psi)
			if err != nil {
				return err
			}

			if err := ctrl.SetControllerReference(request, newPt, c.client.Scheme()); err != nil {
				return err
			}

			if err = c.client.Create(ctx, newPt); err != nil {
				return err
			}
		}
		// maybe check the consistency deeper
	}
	return nil
}

func (c *Controller) requestIsNeeded(ctx context.Context, wl *kueue.Workload, checkName string) bool {
	prc, err := c.helper.ProvReqConfigForAdmissionCheck(ctx, checkName)
	if err != nil {
		return false
	}
	expectdPodsets := requiredPodSets(wl.Spec.PodSets, prc.Spec.ManagedResources)
	return len(expectdPodsets) > 0
}

func requiredPodSets(podSets []kueue.PodSet, resources []corev1.ResourceName) []string {
	resourcesSet := sets.New(resources...)
	users := make([]string, 0, len(podSets))
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
	return false
}

func parametersKueueToProvisioning(in map[string]kueue.Parameter) map[string]autoscaling.Parameter {
	if in == nil {
		return nil
	}

	out := make(map[string]autoscaling.Parameter, len(in))
	for k, v := range in {
		out[k] = autoscaling.Parameter(v)
	}
	return out
}

func requestHasParamaters(req *autoscaling.ProvisioningRequest, prc *kueue.ProvisioningRequestConfig) bool {
	if req.Spec.ProvisioningClassName != prc.Spec.ProvisioningClassName {
		return false
	}
	if len(req.Spec.Parameters) != len(prc.Spec.Parameters) {
		return false
	}
	for k, vReq := range req.Spec.Parameters {
		if vCfg, found := prc.Spec.Parameters[k]; !found || vReq != autoscaling.Parameter(vCfg) {
			return false
		}
	}
	return true
}

func (c *Controller) syncCheckStates(ctx context.Context, wl *kueue.Workload, checks []string) error {
	checksMap := slices.ToRefMap(wl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
	wlPatch := workload.BaseSSAWorkload(wl)
	updated := false
	for _, check := range checks {
		checkState := *checksMap[check]
		if _, err := c.helper.ProvReqConfigForAdmissionCheck(ctx, check); err != nil {
			// the check is not active
			if checkState.State != kueue.CheckStatePending || checkState.Message != CheckInactiveMessage {
				updated = true
				checkState.State = kueue.CheckStatePending
				checkState.Message = CheckInactiveMessage
			}
		} else if !c.requestIsNeeded(ctx, wl, check) {
			if checkState.State != kueue.CheckStateReady {
				updated = true
				checkState.State = kueue.CheckStateReady
				checkState.Message = NoRequestNeeded
				checkState.PodSetUpdates = nil
			}
		} else {
			pr := &autoscaling.ProvisioningRequest{}
			if err := c.client.Get(ctx, types.NamespacedName{Namespace: wl.Namespace, Name: GetProvisioningRequestName(wl.Name, check)}, pr); err != nil {
				return client.IgnoreNotFound(err)
			}

			prFailed := apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.Failed)
			prAccepted := apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.Provisioned)
			prAvailable := apimeta.IsStatusConditionTrue(pr.Status.Conditions, autoscaling.CapacityAvailable)

			switch {
			case prFailed:
				if checkState.State != kueue.CheckStateRejected {
					updated = true
					checkState.State = kueue.CheckStateRejected
					checkState.Message = apimeta.FindStatusCondition(pr.Status.Conditions, autoscaling.Failed).Message
				}
			case prAccepted || prAvailable:
				if checkState.State != kueue.CheckStateReady {
					updated = true
					checkState.State = kueue.CheckStateReady
					// add the pod podSetUpdates
					checkState.PodSetUpdates = podSetUpdates(wl, pr, check)
				}
			default:
				if checkState.State != kueue.CheckStatePending {
					updated = true
					checkState.State = kueue.CheckStatePending
				}
			}
		}
		workload.SetAdmissionCheckState(&wlPatch.Status.AdmissionChecks, checkState)
	}
	if updated {
		c.record.Eventf(wl, corev1.EventTypeNormal, "UpdatedWorkload", "Updated admission checks state for workload %s", workload.Key(wl))
		return c.client.Status().Patch(ctx, wlPatch, client.Apply, client.FieldOwner(ControllerName), client.ForceOwnership)
	}
	return nil
}

func podSetUpdates(wl *kueue.Workload, pr *autoscaling.ProvisioningRequest, checkName string) []kueue.PodSetUpdate {
	podSets := wl.Spec.PodSets
	refMap := slices.ToMap(podSets, func(i int) (string, string) {
		return getProvisioningRequestPodTemplateName(wl.Name, checkName, podSets[i].Name), podSets[i].Name
	})
	return slices.Map(pr.Spec.PodSets, func(ps *autoscaling.PodSet) kueue.PodSetUpdate {
		return kueue.PodSetUpdate{
			Name:        refMap[ps.PodTemplateRef.Name],
			Annotations: map[string]string{ConsumesAnnotationKey: pr.Name},
		}
	})
}

type acHandler struct {
	client client.Client
}

var _ handler.EventHandler = (*acHandler)(nil)

func (a *acHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	ac, isAc := event.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return
	}

	if ac.Spec.ControllerName == ControllerName {
		err := a.reconcileWorkloadsUsing(ctx, ac.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "admissionCheck", klog.KObj(ac))
		}
	}
}

func (a *acHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
	oldAc, isOldAc := event.ObjectOld.(*kueue.AdmissionCheck)
	newAc, isNewAc := event.ObjectNew.(*kueue.AdmissionCheck)
	if !isNewAc || !isOldAc {
		return
	}

	if oldAc.Spec.ControllerName == ControllerName || newAc.Spec.ControllerName == ControllerName {
		err := a.reconcileWorkloadsUsing(ctx, oldAc.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on update event", "admissionCheck", klog.KObj(oldAc))
		}
	}
}

func (a *acHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	ac, isAc := event.Object.(*kueue.AdmissionCheck)
	if !isAc {
		return
	}

	if ac.Spec.ControllerName == ControllerName {
		err := a.reconcileWorkloadsUsing(ctx, ac.Name, q)
		if err != nil {
			ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "admissionCheck", klog.KObj(ac))
		}
	}
}

func (a *acHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	// nothing to do for now
}

func (a *acHandler) reconcileWorkloadsUsing(ctx context.Context, check string, q workqueue.RateLimitingInterface) error {
	list := &kueue.WorkloadList{}
	if err := a.client.List(ctx, list, client.MatchingFields{WorkloadsWithAdmissionCheckKey: check}); client.IgnoreNotFound(err) != nil {
		return err
	}

	for i := range list.Items {
		wl := &list.Items[i]
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
	helper            *storeHelper
	acHandlerOverride func(ctx context.Context, config string, q workqueue.RateLimitingInterface) error
}

var _ handler.EventHandler = (*prcHandler)(nil)

func (p *prcHandler) Create(ctx context.Context, event event.CreateEvent, q workqueue.RateLimitingInterface) {
	prc, isPRC := event.Object.(*kueue.ProvisioningRequestConfig)
	if !isPRC {
		return
	}
	err := p.reconcileWorkloadsUsing(ctx, prc.Name, q)
	if err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on create event", "provisioningRequestConfig", klog.KObj(prc))
	}
}

func (p *prcHandler) Update(ctx context.Context, event event.UpdateEvent, q workqueue.RateLimitingInterface) {
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

func (p *prcHandler) Delete(ctx context.Context, event event.DeleteEvent, q workqueue.RateLimitingInterface) {
	prc, isPRC := event.Object.(*kueue.ProvisioningRequestConfig)
	if !isPRC {
		return
	}
	err := p.reconcileWorkloadsUsing(ctx, prc.Name, q)
	if err != nil {
		ctrl.LoggerFrom(ctx).V(5).Error(err, "Failure on delete event", "provisioningRequestConfig", klog.KObj(prc))
	}
}

func (p *prcHandler) Generic(_ context.Context, _ event.GenericEvent, _ workqueue.RateLimitingInterface) {
	// nothing to do for now
}

func (p *prcHandler) reconcileWorkloadsUsing(ctx context.Context, config string, q workqueue.RateLimitingInterface) error {
	users, err := p.helper.AdmissionChecksUsingProvReqConfig(ctx, config)
	if err != nil {
		return err
	}
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
		helper:            c.helper,
		acHandlerOverride: ach.reconcileWorkloadsUsing,
	}
	err := ctrl.NewControllerManagedBy(mgr).
		For(&kueue.Workload{}).
		Owns(&autoscaling.ProvisioningRequest{}).
		Watches(&kueue.AdmissionCheck{}, ach).
		Watches(&kueue.ProvisioningRequestConfig{}, prch).
		Complete(c)
	if err != nil {
		return err
	}

	prcACh := &prcHandler{
		helper: c.helper,
	}
	acReconciler := &acReconciler{
		client: c.client,
		helper: c.helper,
		record: c.record,
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kueue.AdmissionCheck{}).
		Watches(&kueue.ProvisioningRequestConfig{}, prcACh).
		Complete(acReconciler)
}

func GetProvisioningRequestName(workloadName, checkName string) string {
	fullName := fmt.Sprintf("%s-%s", workloadName, checkName)
	return limitObjectName(fullName)
}

func getProvisioningRequestPodTemplateName(workloadName, checkName, podsetName string) string {
	fullName := fmt.Sprintf("%s-%s-%s-%s", podTemplatesPrefix, workloadName, checkName, podsetName)
	return limitObjectName(fullName)
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
