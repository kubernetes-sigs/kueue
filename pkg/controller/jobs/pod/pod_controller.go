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

package pod

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

const (
	SchedulingGateName             = "kueue.x-k8s.io/admission"
	FrameworkName                  = "pod"
	gateNotFound                   = -1
	ConditionTypeTerminationTarget = "TerminationTarget"
	errMsgIncorrectGroupRoleCount  = "pod group can't include more than 8 roles"
)

var (
	gvk = corev1.SchemeGroupVersion.WithKind("Pod")
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:  SetupIndexes,
		NewReconciler: NewReconciler,
		SetupWebhook:  SetupWebhook,
		JobType:       &corev1.Pod{},
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

var NewReconciler = jobframework.NewGenericReconciler(
	func() jobframework.GenericJob {
		return &Pod{}
	}, nil)

type Pod struct {
	pod     corev1.Pod
	isGroup bool
	list    corev1.PodList
}

var (
	_ jobframework.GenericJob        = (*Pod)(nil)
	_ jobframework.JobWithCustomStop = (*Pod)(nil)
	_ jobframework.JobWithFinalize   = (*Pod)(nil)
	_ jobframework.ComposableJob     = (*Pod)(nil)
)

func fromObject(o runtime.Object) *Pod {
	out := Pod{}
	out.pod = *o.(*corev1.Pod)
	return &out
}

// Object returns the job instance.
func (p *Pod) Object() client.Object {
	return &p.pod
}

// gateIndex returns the index of the Kueue scheduling gate for corev1.Pod.
// If the scheduling gate is not found, returns -1.
func gateIndex(p *corev1.Pod) int {
	for i := range p.Spec.SchedulingGates {
		if p.Spec.SchedulingGates[i].Name == SchedulingGateName {
			return i
		}
	}
	return gateNotFound
}

func podSuspended(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded || gateIndex(p) != gateNotFound
}

// IsSuspended returns whether the job is suspended or not.
func (p *Pod) IsSuspended() bool {
	return podSuspended(&p.pod)
}

// Suspend will suspend the job.
func (p *Pod) Suspend() {
	// Not implemented because this is not called when JobWithCustomStop is implemented.
}

// RunWithPodSetsInfo will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) RunWithPodSetsInfo(podSetsInfo []podset.PodSetInfo) error {
	if p.groupName() == "" && len(podSetsInfo) != 1 {
		return fmt.Errorf("%w: expecting 1 pod set got %d", podset.ErrInvalidPodsetInfo, len(podSetsInfo))
	}
	idx := gateIndex(&p.pod)
	if idx != gateNotFound {
		p.pod.Spec.SchedulingGates = append(p.pod.Spec.SchedulingGates[:idx], p.pod.Spec.SchedulingGates[idx+1:]...)
	}
	return podset.Merge(&p.pod.ObjectMeta, &p.pod.Spec, podSetsInfo[0])
}

// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
func (p *Pod) RestorePodSetsInfo(nodeSelectors []podset.PodSetInfo) bool {
	// Not implemented since Pods cannot be updated, they can only be terminated.
	return false
}

// Finished means whether the job is completed/failed or not,
// condition represents the workload finished condition.
func (p *Pod) Finished() (metav1.Condition, bool) {
	finished := true
	hasFailed := false
	succeededCount := 0
	groupTotalCount := 0

	if !p.isGroup {
		ph := p.pod.Status.Phase
		finished = ph == corev1.PodSucceeded || ph == corev1.PodFailed
		hasFailed = ph == corev1.PodFailed
	} else {
		var err error
		if groupTotalCount, err = p.groupTotalCount(); err != nil {
			ctrl.Log.V(2).Error(err, "failed to check if pod group is finished")
			return metav1.Condition{}, false
		}

		for i := range p.list.Items {
			ph := p.list.Items[i].Status.Phase
			if ph == corev1.PodSucceeded {
				succeededCount++
			}
		}
	}
	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: "Job finished successfully",
	}

	if !p.isGroup {
		if hasFailed {
			condition.Message = "Job failed"
		}
	} else {
		if succeededCount < groupTotalCount {
			return metav1.Condition{}, false
		} else {
			condition.Message = fmt.Sprintf("Pods succeeded: %d/%d.", succeededCount, groupTotalCount)
		}
	}

	return condition, finished
}

// PodSets will build workload podSets corresponding to the job.
func (p *Pod) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Name:  kueue.DefaultPodSetName,
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: *p.pod.Spec.DeepCopy(),
			},
		},
	}
}

// IsActive returns true if there are any running pods.
func (p *Pod) IsActive() bool {
	for i := range p.list.Items {
		if p.list.Items[i].Status.Phase == corev1.PodRunning {
			return true
		}
	}
	return false
}

func hasPodReadyTrue(conds []corev1.PodCondition) bool {
	for i := range conds {
		c := conds[i]
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// PodsReady instructs whether job derived pods are all ready now.
func (p *Pod) PodsReady() bool {
	if !p.isGroup {
		return hasPodReadyTrue(p.pod.Status.Conditions)
	}

	for i := range p.list.Items {
		if !hasPodReadyTrue(p.list.Items[i].Status.Conditions) {
			return false
		}
	}
	return true
}

// GVK returns GVK (Group Version Kind) for the job.
func (p *Pod) GVK() schema.GroupVersionKind {
	return gvk
}

func (p *Pod) Stop(ctx context.Context, c client.Client, _ []podset.PodSetInfo, stopReason jobframework.StopReason, eventMsg string) (bool, error) {
	var podsInGroup []corev1.Pod

	if p.isGroup {
		podsInGroup = p.list.Items
	} else {
		podsInGroup = []corev1.Pod{p.pod}
	}

	for i := range podsInGroup {
		// If the workload is being deleted, delete even finished Pods.
		if !podsInGroup[i].DeletionTimestamp.IsZero() || (stopReason != jobframework.StopReasonWorkloadDeleted && podSuspended(&podsInGroup[i])) {
			continue
		}
		podInGroup := fromObject(&podsInGroup[i])

		// The podset info is not relevant here, since this should mark the pod's end of life
		pCopy := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:       podInGroup.pod.UID,
				Name:      podInGroup.pod.Name,
				Namespace: podInGroup.pod.Namespace,
			},
			TypeMeta: podInGroup.pod.TypeMeta,
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   ConditionTypeTerminationTarget,
						Status: corev1.ConditionTrue,
						LastTransitionTime: metav1.Time{
							Time: time.Now(),
						},
						Reason:  "StoppedByKueue",
						Message: eventMsg,
					},
				},
			},
		}
		if err := c.Status().Patch(ctx, pCopy, client.Apply, client.FieldOwner(constants.KueueName)); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if err := c.Delete(ctx, podInGroup.Object()); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}

	// If related workload is deleted, the generic reconciler will stop the pod group and finalize the workload.
	// However, it won't finalize the pods. Since the Stop method for the pod group deletes all the pods in the
	// group, the pods will be finalized here.
	if p.isGroup && stopReason == jobframework.StopReasonWorkloadDeleted {
		err := p.Finalize(ctx, c)
		if err != nil {
			return false, err
		}
	}

	return true, nil
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk)
}

func (p *Pod) Finalize(ctx context.Context, c client.Client) error {
	groupName := p.groupName()

	var podsInGroup corev1.PodList
	if groupName == "" {
		podsInGroup.Items = append(podsInGroup.Items, *p.Object().(*corev1.Pod))
	} else {
		if err := c.List(ctx, &podsInGroup, client.MatchingLabels{
			GroupNameLabel: groupName,
		}, client.InNamespace(p.pod.Namespace)); err != nil {
			return err
		}
	}

	for _, pod := range podsInGroup.Items {
		if controllerutil.RemoveFinalizer(&pod, PodFinalizer) {
			if err := c.Update(ctx, &pod); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *Pod) Skip() bool {
	// Skip pod reconciliation, if managed label is not set
	if v, ok := p.pod.GetLabels()[ManagedLabelKey]; !ok || v != ManagedLabelValue {
		return true
	}
	return false
}

func (p *Pod) groupName() string {
	return p.Object().GetLabels()[GroupNameLabel]
}

func (p *Pod) groupTotalCount() (int, error) {
	if p.groupName() == "" {
		return 0, fmt.Errorf("pod doesn't have a '%s' label", GroupNameLabel)
	}

	gtcAnnotation, ok := p.Object().GetAnnotations()[GroupTotalCountAnnotation]
	if !ok {
		return 0, fmt.Errorf("failed to extract '%s' annotation",
			GroupTotalCountAnnotation)
	}

	gtc, err := strconv.Atoi(gtcAnnotation)
	if err != nil {
		return 0, err
	}

	if gtc < 1 {
		return 0, fmt.Errorf("incorrect annotation value '%s=%s': group total count should be greater than zero",
			GroupTotalCountAnnotation, gtcAnnotation)
	}

	return gtc, nil
}

// Load loads all pods in the group
func (p *Pod) Load(ctx context.Context, c client.Client, key types.NamespacedName) (removeFinalizers bool, err error) {
	if err := c.Get(ctx, key, &p.pod); err != nil {
		return apierrors.IsNotFound(err), err
	}
	groupName := p.groupName()
	p.isGroup = groupName != ""
	if !p.isGroup {
		return !p.pod.DeletionTimestamp.IsZero(), nil
	}

	if err := c.List(ctx, &p.list, client.MatchingLabels{
		GroupNameLabel: groupName,
	}, client.InNamespace(key.Namespace)); err != nil {
		return false, err
	}

	return false, nil
}

func (p *Pod) constructGroupPodSets(podsInGroup corev1.PodList) ([]kueue.PodSet, error) {
	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		return nil, err
	}
	originalQueue := jobframework.QueueName(p)

	var resultPodSets []kueue.PodSet

	for _, podInGroup := range podsInGroup.Items {
		// Skip failed pods
		if podInGroup.Status.Phase == corev1.PodFailed {
			continue
		}

		if podInGroupQueue := jobframework.QueueNameForObject(&podInGroup); podInGroupQueue != originalQueue {
			return nil, jobframework.UnretryableError(fmt.Sprintf("pods '%s' and '%s' has different queue names: %s!=%s",
				p.pod.GetName(), podInGroup.GetName(),
				originalQueue, podInGroupQueue))
		}

		tc, err := strconv.Atoi(podInGroup.GetAnnotations()[GroupTotalCountAnnotation])
		if err != nil {
			return nil, fmt.Errorf("failed to extract '%s' annotation from the pod '%s': %w",
				GroupTotalCountAnnotation,
				podInGroup.GetName(),
				err)
		}
		if tc != groupTotalCount {
			return nil, jobframework.UnretryableError(fmt.Sprintf("pods '%s' and '%s' has different '%s' values: %d!=%d",
				p.pod.GetName(), podInGroup.GetName(),
				GroupTotalCountAnnotation,
				groupTotalCount, tc))
		}

		roleHash, ok := podInGroup.Annotations[RoleHashAnnotation]
		if !ok {
			roleHash, err = getRoleHash(fromObject(&podInGroup))
			if err != nil {
				return nil, fmt.Errorf("failed to calculate role hash for the pod with no '%s' annotation. %w", RoleHashAnnotation, err)
			}
		}

		podRoleFound := false
		for psi := range resultPodSets {
			if resultPodSets[psi].Name == roleHash {
				podRoleFound = true
				resultPodSets[psi].Count++
			}
		}

		if !podRoleFound {
			podSet := fromObject(&podInGroup).PodSets()
			podSet[0].Name = roleHash

			resultPodSets = append(resultPodSets, podSet[0])
		}
	}

	slices.SortFunc(resultPodSets, func(a, b kueue.PodSet) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return resultPodSets, nil
}

func (p *Pod) ConstructComposableWorkload(ctx context.Context, c client.Client, r record.EventRecorder) (*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)
	object := p.Object()

	var (
		podSets []kueue.PodSet
	)

	if !p.isGroup {
		podSets = p.PodSets()
	} else {
		groupTotalCount, err := p.groupTotalCount()
		if err != nil {
			return nil, err
		}

		if len(p.list.Items) != groupTotalCount {
			errMsg := fmt.Sprintf("'%s' group total count is different from the actual number of pods in the cluster", p.groupName())
			r.Eventf(object, corev1.EventTypeWarning, "ErrWorkloadCompose", errMsg)
			return nil, jobframework.UnretryableError(errMsg)
		}

		podSets, err = p.constructGroupPodSets(p.list)
		if err != nil {
			if jobframework.IsUnretryableError(err) {
				r.Eventf(object, corev1.EventTypeWarning, "ErrWorkloadCompose", err.Error())
			}
			return nil, err
		}

		if len(podSets) > 8 {
			return nil, jobframework.UnretryableError(errMsgIncorrectGroupRoleCount)
		}
	}

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  p.pod.GetNamespace(),
			Labels:     map[string]string{},
			Finalizers: []string{kueue.ResourceInUseFinalizerName},
		},
		Spec: kueue.WorkloadSpec{
			PodSets:   podSets,
			QueueName: jobframework.QueueName(p),
		},
	}

	if !p.isGroup {
		wl.Name = jobframework.GetWorkloadNameForOwnerWithGVK(p.pod.GetName(), p.GVK())
		jobUid := string(p.Object().GetUID())
		if errs := validation.IsValidLabelValue(jobUid); len(errs) == 0 {
			wl.Labels[controllerconsts.JobUIDLabel] = jobUid
		} else {
			log.V(2).Info(
				"Validation of the owner job UID label has failed. Creating workload without the label.",
				"ValidationErrors", errs,
				"LabelValue", jobUid,
			)
		}

		// add the controller ref
		if err := controllerutil.SetControllerReference(p.Object(), wl, c.Scheme()); err != nil {
			return nil, err
		}
	} else {
		wl.Name = p.groupName()
		for _, pod := range p.list.Items {
			if err := controllerutil.SetOwnerReference(&pod, wl, c.Scheme()); err != nil {
				return nil, err
			}
		}
	}

	return wl, nil
}

func (p *Pod) FindMatchingWorkloads(ctx context.Context, c client.Client) (*kueue.Workload, []*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)
	groupName := p.groupName()

	if groupName == "" {
		return jobframework.FindMatchingWorkloads(ctx, c, p)
	}

	// Find a matching workload first if there is one.
	workload := &kueue.Workload{}
	if err := c.Get(ctx, types.NamespacedName{Name: p.groupName(), Namespace: p.pod.GetNamespace()}, workload); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		log.Error(err, "Unable to get related workload")
		return nil, nil, err
	}

	jobPodSets, err := p.constructGroupPodSets(p.list)
	if err != nil {
		return nil, nil, err
	}

	if p.equivalentToWorkload(workload, jobPodSets) {
		return workload, []*kueue.Workload{}, nil
	} else {
		return nil, []*kueue.Workload{workload}, nil
	}
}

func (p *Pod) equivalentToWorkload(wl *kueue.Workload, jobPodSets []kueue.PodSet) bool {
	workloadFinished := apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)

	if wl.GetName() != p.groupName() {
		return false
	}

	if !workloadFinished && len(wl.Spec.PodSets) < len(jobPodSets) {
		return false
	}

	for i := range wl.Spec.PodSets {
		if i >= len(jobPodSets) {
			return true
		}
		if !workloadFinished && wl.Spec.PodSets[i].Count < jobPodSets[i].Count {
			return false
		}
		if wl.Spec.PodSets[i].Name != jobPodSets[i].Name {
			return false
		}
	}

	return true
}

func IsPodOwnerManagedByKueue(p *Pod) bool {
	if owner := metav1.GetControllerOf(&p.pod); owner != nil {
		return jobframework.IsOwnerManagedByKueue(owner) || (owner.Kind == "RayCluster" && strings.HasPrefix(owner.APIVersion, "ray.io/v1alpha1"))
	}
	return false
}

func GetWorkloadNameForPod(podName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(podName, gvk)
}
