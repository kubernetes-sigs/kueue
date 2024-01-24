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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
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
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	SchedulingGateName             = "kueue.x-k8s.io/admission"
	FrameworkName                  = "pod"
	gateNotFound                   = -1
	ConditionTypeTerminationTarget = "TerminationTarget"
	errMsgIncorrectGroupRoleCount  = "pod group can't include more than 8 roles"
	IsGroupWorkloadAnnotationKey   = "kueue.x-k8s.io/is-group-workload"
	IsGroupWorkloadAnnotationValue = "true"
)

var (
	gvk                          = corev1.SchemeGroupVersion.WithKind("Pod")
	errIncorrectReconcileRequest = fmt.Errorf("event handler error: got a single pod reconcile request for a pod group")
	errPendingOps                = jobframework.UnretryableError("waiting to observe previous operations on pods")
	errPodNoSupportKubeVersion   = errors.New("pod integration only supported in Kubernetes 1.27 or newer")
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:          SetupIndexes,
		NewReconciler:         NewReconciler,
		SetupWebhook:          SetupWebhook,
		JobType:               &corev1.Pod{},
		CanSupportIntegration: CanSupportIntegration,
	}))
}

// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=list;get;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=pods/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch

type Reconciler struct {
	*jobframework.JobReconciler
	expectationsStore *expectationsStore
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.ReconcileGenericJob(ctx, req, &Pod{excessPodExpectations: r.expectationsStore})
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Watches(&corev1.Pod{}, &podEventHandler{cleanedUpPodsExpectations: r.expectationsStore}).Named("v1_pod").
		Watches(&kueue.Workload{}, &workloadHandler{}).
		Complete(r)
}

func NewReconciler(c client.Client, record record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &Reconciler{
		JobReconciler:     jobframework.NewReconciler(c, record, opts...),
		expectationsStore: newUIDExpectations("finalizedPods"),
	}
}

type Pod struct {
	pod                   corev1.Pod
	key                   types.NamespacedName
	isFound               bool
	isGroup               bool
	unretriableGroup      *bool
	list                  corev1.PodList
	excessPodExpectations *expectationsStore
	satisfiedExcessPods   bool
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

func isPodTerminated(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded
}

func podSuspended(p *corev1.Pod) bool {
	return isPodTerminated(p) || gateIndex(p) != gateNotFound
}

func isUnretriablePod(pod corev1.Pod) bool {
	return pod.Annotations[RetriableInGroupAnnotation] == "false"
}

// isUnretriableGroup returns true if at least one pod in the group
// has a RetriableInGroupAnnotation set to 'false'.
func (p *Pod) isUnretriableGroup() bool {
	if p.unretriableGroup != nil {
		return *p.unretriableGroup
	}

	for _, pod := range p.list.Items {
		if isUnretriablePod(pod) {
			p.unretriableGroup = ptr.To(true)
			return true
		}
	}

	p.unretriableGroup = ptr.To(false)
	return false
}

// IsSuspended returns whether the job is suspended or not.
func (p *Pod) IsSuspended() bool {
	if !p.isGroup {
		return podSuspended(&p.pod)
	}

	for i := range p.list.Items {
		if podSuspended(&p.list.Items[i]) {
			return true
		}
	}

	return false
}

// Suspend will suspend the job.
func (p *Pod) Suspend() {
	// Not implemented because this is not called when JobWithCustomStop is implemented.
}

// ungatePod removes the kueue scheduling gate from the pod.
// Returns true if the pod has been ungated and false otherwise.
func ungatePod(pod *corev1.Pod) bool {
	idx := gateIndex(pod)
	if idx != gateNotFound {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates[:idx], pod.Spec.SchedulingGates[idx+1:]...)
		return true
	}

	return false
}

// Run will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) Run(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, recorder record.EventRecorder, msg string) error {
	log := ctrl.LoggerFrom(ctx)

	if !p.isGroup {
		if len(podSetsInfo) != 1 {
			return fmt.Errorf("%w: expecting 1 pod set got %d", podset.ErrInvalidPodsetInfo, len(podSetsInfo))
		}

		if ungated := ungatePod(&p.pod); !ungated {
			return nil
		}

		if err := podset.Merge(&p.pod.ObjectMeta, &p.pod.Spec, podSetsInfo[0]); err != nil {
			return err
		}

		err := c.Update(ctx, &p.pod)
		if err != nil {
			return err
		}
		if recorder != nil {
			recorder.Event(&p.pod, corev1.EventTypeNormal, jobframework.ReasonStarted, msg)
		}
		return nil
	}

	var podsToUngate []*corev1.Pod

	for i := range p.list.Items {
		pod := &p.list.Items[i]
		if ungated := ungatePod(pod); !ungated {
			continue
		}
		podsToUngate = append(podsToUngate, pod)
	}
	if len(podsToUngate) == 0 {
		return nil
	}

	return parallelize.Until(ctx, len(podsToUngate), func(i int) error {
		pod := podsToUngate[i]
		roleHash, err := getRoleHash(*pod)
		if err != nil {
			return err
		}

		podSetIndex := slices.IndexFunc(podSetsInfo, func(info podset.PodSetInfo) bool {
			return info.Name == roleHash
		})
		if podSetIndex == -1 {
			return fmt.Errorf("%w: podSetInfo with the name '%s' is not found", podset.ErrInvalidPodsetInfo, roleHash)
		}

		err = podset.Merge(&pod.ObjectMeta, &pod.Spec, podSetsInfo[podSetIndex])
		if err != nil {
			return err
		}

		log.V(3).Info("Starting pod in group", "podInGroup", klog.KObj(pod))
		if err := c.Update(ctx, pod); err != nil {
			return err
		}
		if recorder != nil {
			recorder.Event(pod, corev1.EventTypeNormal, jobframework.ReasonStarted, msg)
		}
		return nil
	})

}

// RunWithPodSetsInfo will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) RunWithPodSetsInfo(_ []podset.PodSetInfo) error {
	// Not implemented because this is not called when JobWithCustomRun is implemented.
	return fmt.Errorf("RunWithPodSetsInfo is not implemented for the Pod object")
}

// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
func (p *Pod) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	// Not implemented since Pods cannot be updated, they can only be terminated.
	return false
}

// Finished means whether the job is completed/failed or not,
// condition represents the workload finished condition.
func (p *Pod) Finished() (metav1.Condition, bool) {
	finished := true

	condition := metav1.Condition{
		Type:    kueue.WorkloadFinished,
		Status:  metav1.ConditionTrue,
		Reason:  "JobFinished",
		Message: "Job finished successfully",
	}

	if !p.isGroup {
		ph := p.pod.Status.Phase
		finished = ph == corev1.PodSucceeded || ph == corev1.PodFailed

		if ph == corev1.PodFailed {
			condition.Message = "Job failed"
		}

		return condition, finished
	}
	isActive := false
	succeededCount := 0

	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		ctrl.Log.V(2).Error(err, "failed to check if pod group is finished")
		return metav1.Condition{}, false
	}
	for _, pod := range p.list.Items {
		if pod.Status.Phase == corev1.PodSucceeded {
			succeededCount++
		}

		if !isPodTerminated(&pod) {
			isActive = true
		}
	}

	unretriableGroup := p.isUnretriableGroup()

	if succeededCount == groupTotalCount || (!isActive && unretriableGroup) {
		condition.Message = fmt.Sprintf("Pods succeeded: %d/%d.", succeededCount, groupTotalCount)
	} else {
		return metav1.Condition{}, false
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

func CanSupportIntegration(opts ...jobframework.Option) (bool, error) {
	options := jobframework.ProcessOptions(opts...)

	v := options.KubeServerVersion.GetServerVersion()
	if v.String() == "" || v.LessThan(kubeversion.KubeVersion1_27) {
		return false, fmt.Errorf("kubernetesVersion %q: %w", v.String(), errPodNoSupportKubeVersion)
	}
	return true, nil
}

func (p *Pod) Finalize(ctx context.Context, c client.Client) error {
	groupName := podGroupName(p.pod)

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

	return parallelize.Until(ctx, len(podsInGroup.Items), func(i int) error {
		pod := &podsInGroup.Items[i]
		if controllerutil.RemoveFinalizer(pod, PodFinalizer) {
			return c.Update(ctx, pod)
		}
		return nil
	})
}

func (p *Pod) Skip() bool {
	// Skip pod reconciliation, if pod is found, and it's managed label is not set or incorrect.
	if v, ok := p.pod.GetLabels()[ManagedLabelKey]; p.isFound && (!ok || v != ManagedLabelValue) {
		return true
	}
	return false
}

// podGroupName returns a value of GroupNameLabel for the pod object.
// Returns an empty string if there's no such label.
func podGroupName(p corev1.Pod) string {
	return p.GetLabels()[GroupNameLabel]
}

// groupTotalCount returns the value of GroupTotalCountAnnotation for the pod being reconciled at the moment.
// It doesn't check if the whole group has the same total group count annotation value.
func (p *Pod) groupTotalCount() (int, error) {
	if podGroupName(p.pod) == "" {
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

// getRoleHash will filter all the fields of the pod that are relevant to admission (pod role) and return a sha256
// checksum of those fields. This is used to group the pods of the same roles when interacting with the workload.
func getRoleHash(p corev1.Pod) (string, error) {
	if roleHash, ok := p.Annotations[RoleHashAnnotation]; ok {
		return roleHash, nil
	}

	shape := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": omitKueueLabels(p.ObjectMeta.Labels),
		},
		"spec": map[string]interface{}{
			"initContainers":            containersShape(p.Spec.InitContainers),
			"containers":                containersShape(p.Spec.Containers),
			"nodeSelector":              p.Spec.NodeSelector,
			"affinity":                  p.Spec.Affinity,
			"tolerations":               p.Spec.Tolerations,
			"runtimeClassName":          p.Spec.RuntimeClassName,
			"priority":                  p.Spec.Priority,
			"preemptionPolicy":          p.Spec.PreemptionPolicy,
			"topologySpreadConstraints": p.Spec.TopologySpreadConstraints,
			"overhead":                  p.Spec.Overhead,
			"volumes":                   volumesShape(p.Spec.Volumes),
			"resourceClaims":            p.Spec.ResourceClaims,
		},
	}

	shapeJson, err := json.Marshal(shape)
	if err != nil {
		return "", err
	}

	// Trim hash to 8 characters and return
	return fmt.Sprintf("%x", sha256.Sum256(shapeJson))[:8], nil
}

// Load loads all pods in the group
func (p *Pod) Load(ctx context.Context, c client.Client, key *types.NamespacedName) (removeFinalizers bool, err error) {
	nsKey := strings.Split(key.Namespace, "/")

	if len(nsKey) == 1 {
		if err := c.Get(ctx, *key, &p.pod); err != nil {
			return apierrors.IsNotFound(err), err
		}
		p.isFound = true

		// If the key.Namespace doesn't contain a "group/" prefix, even though
		// the pod has a group name, there's something wrong with the event handler.
		if podGroupName(p.pod) != "" {
			return false, errIncorrectReconcileRequest
		}

		return !p.pod.DeletionTimestamp.IsZero(), nil
	}

	p.isGroup = true

	key.Namespace = nsKey[1]
	p.key = *key

	// Check the expectations before listing pods, otherwise a new pod can sneak in
	// and update the expectations after we've retrieved active pods from the store.
	p.satisfiedExcessPods = p.excessPodExpectations.Satisfied(ctrl.LoggerFrom(ctx), *key)

	if err := c.List(ctx, &p.list, client.MatchingLabels{
		GroupNameLabel: key.Name,
	}, client.InNamespace(key.Namespace)); err != nil {
		return false, err
	}

	if len(p.list.Items) > 0 {
		p.isFound = true
		p.pod = p.list.Items[0]
		key.Name = p.pod.Name
	}

	// If none of the pods in group are found,
	// the respective workload should be finalized
	return !p.isFound, nil
}

func (p *Pod) constructGroupPodSets() ([]kueue.PodSet, error) {
	var resultPodSets []kueue.PodSet

	for _, podInGroup := range p.list.Items {
		if !isPodRunnableOrSucceeded(&podInGroup) {
			continue
		}

		roleHash, err := getRoleHash(podInGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate pod role hash: %w", err)
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

// validatePodGroupMetadata validates metadata of all members of the pod group
func (p *Pod) validatePodGroupMetadata(r record.EventRecorder, activePods []corev1.Pod) error {
	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		return err
	}
	originalQueue := jobframework.QueueName(p)

	if len(activePods) < groupTotalCount {
		errMsg := fmt.Sprintf("'%s' group total count is less than the actual number of pods in the cluster", podGroupName(p.pod))
		r.Eventf(p.Object(), corev1.EventTypeWarning, jobframework.ReasonErrWorkloadCompose, errMsg)
		return jobframework.UnretryableError(errMsg)
	}

	for _, podInGroup := range p.list.Items {
		// Skip failed pods
		if podInGroup.Status.Phase == corev1.PodFailed {
			continue
		}

		if podInGroupQueue := jobframework.QueueNameForObject(&podInGroup); podInGroupQueue != originalQueue {
			return jobframework.UnretryableError(fmt.Sprintf("pods '%s' and '%s' has different queue names: %s!=%s",
				p.pod.GetName(), podInGroup.GetName(),
				originalQueue, podInGroupQueue))
		}

		tc, err := strconv.Atoi(podInGroup.GetAnnotations()[GroupTotalCountAnnotation])
		if err != nil {
			return fmt.Errorf("failed to extract '%s' annotation from the pod '%s': %w",
				GroupTotalCountAnnotation,
				podInGroup.GetName(),
				err)
		}
		if tc != groupTotalCount {
			return jobframework.UnretryableError(fmt.Sprintf("pods '%s' and '%s' has different '%s' values: %d!=%d",
				p.pod.GetName(), podInGroup.GetName(),
				GroupTotalCountAnnotation,
				groupTotalCount, tc))
		}
	}

	return nil
}

// runnableOrSucceededPods returns a slice of active pods in the group
func (p *Pod) runnableOrSucceededPods() []corev1.Pod {
	activePods := make([]corev1.Pod, 0, len(p.list.Items))
	for _, pod := range p.list.Items {
		if isPodRunnableOrSucceeded(&pod) {
			activePods = append(activePods, pod)
		}
	}

	return activePods
}

// isPodRunnableOrSucceeded returns whether the Pod can eventually run, is Running or Succeeded.
// A Pod cannot run if it's gated and has a deletionTimestamp.
func isPodRunnableOrSucceeded(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil && len(p.Spec.SchedulingGates) > 0 {
		return false
	}
	return p.Status.Phase != corev1.PodFailed
}

// cleanupExcessPods will delete and finalize pods created last if the number of
// activePods is greater than the totalCount value.
func (p *Pod) cleanupExcessPods(ctx context.Context, c client.Client, totalCount int, activePods []corev1.Pod) error {
	log := ctrl.LoggerFrom(ctx)

	extraPodsCount := len(activePods) - totalCount

	if extraPodsCount <= 0 {
		return nil
	}
	// Do not clean up more pods until observing previous operations
	if !p.satisfiedExcessPods {
		return errPendingOps
	}

	// Sort active pods by creation timestamp
	sort.Slice(activePods, func(i, j int) bool {
		pi := &activePods[i]
		pj := &activePods[j]
		iFin := slices.Contains(pi.Finalizers, PodFinalizer)
		jFin := slices.Contains(pj.Finalizers, PodFinalizer)
		// Prefer to keep pods that have a finalizer.
		if iFin != jFin {
			return iFin
		}
		iGated := gateIndex(pi) != gateNotFound
		jGated := gateIndex(pj) != gateNotFound
		// Prefer to keep pods that aren't gated.
		if iGated != jGated {
			return !iGated
		}
		return pi.ObjectMeta.CreationTimestamp.Before(&pj.ObjectMeta.CreationTimestamp)
	})

	// Extract all the latest created extra pods
	extraPods := activePods[len(activePods)-extraPodsCount:]
	extraPodsUIDs := utilslices.Map(extraPods, func(p *corev1.Pod) types.UID { return p.UID })
	p.excessPodExpectations.ExpectUIDs(log, p.key, extraPodsUIDs)

	// Finalize and delete the active pods created last

	err := parallelize.Until(ctx, len(extraPods), func(i int) error {
		pod := extraPods[i]
		if controllerutil.RemoveFinalizer(&pod, PodFinalizer) {
			log.V(3).Info("Finalizing excess pod in group", "excessPod", klog.KObj(&pod))
			if err := c.Update(ctx, &pod); err != nil {
				// We won't observe this cleanup in the event handler.
				p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
				return err
			}
		}
		if pod.ObjectMeta.DeletionTimestamp.IsZero() {
			log.V(3).Info("Deleting excess pod in group", "excessPod", klog.KObj(&pod))
			if err := c.Delete(ctx, &pod); err != nil {
				// We won't observe this cleanup in the event handler.
				p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Remove excess pods from the group list
	newPodsInGroup := make([]corev1.Pod, 0, len(p.list.Items)-len(extraPods))
	for i := range p.list.Items {
		found := false
		for j := range extraPods {
			if p.list.Items[i].Name == extraPods[j].Name && p.list.Items[i].Namespace == extraPods[j].Namespace {
				found = true
				break
			}
		}

		if !found {
			newPodsInGroup = append(newPodsInGroup, p.list.Items[i])
		}
	}
	p.list.Items = newPodsInGroup

	return nil
}

func (p *Pod) ConstructComposableWorkload(ctx context.Context, c client.Client, r record.EventRecorder) (*kueue.Workload, error) {
	object := p.Object()
	log := ctrl.LoggerFrom(ctx)

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  p.pod.GetNamespace(),
			Labels:     map[string]string{},
			Finalizers: []string{kueue.ResourceInUseFinalizerName},
		},
		Spec: kueue.WorkloadSpec{
			QueueName: jobframework.QueueName(p),
		},
	}

	// Construct workload for a single pod
	if !p.isGroup {
		wl.Spec.PodSets = p.PodSets()

		wl.Name = jobframework.GetWorkloadNameForOwnerWithGVK(p.pod.GetName(), p.GVK())
		jobUid := string(object.GetUID())
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
		if err := controllerutil.SetControllerReference(object, wl, c.Scheme()); err != nil {
			return nil, err
		}

		return wl, nil
	}

	if err := p.finalizeNonRunnableNorSucceededPods(ctx, c); err != nil {
		return nil, err
	}

	activePods := p.runnableOrSucceededPods()

	if wl.Annotations == nil {
		wl.Annotations = make(map[string]string)
	}
	wl.Annotations[IsGroupWorkloadAnnotationKey] = IsGroupWorkloadAnnotationValue

	err := p.validatePodGroupMetadata(r, activePods)
	if err != nil {
		return nil, err
	}

	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		return nil, err
	}

	// Cleanup extra pods if there's any
	err = p.cleanupExcessPods(ctx, c, groupTotalCount, activePods)
	if err != nil {
		return nil, err
	}

	// Construct workload for a pod group
	wl.Spec.PodSets, err = p.constructGroupPodSets()
	if err != nil {
		if jobframework.IsUnretryableError(err) {
			r.Eventf(object, corev1.EventTypeWarning, jobframework.ReasonErrWorkloadCompose, err.Error())
		}
		return nil, err
	}

	if len(wl.Spec.PodSets) > 8 {
		return nil, jobframework.UnretryableError(errMsgIncorrectGroupRoleCount)
	}

	wl.Name = podGroupName(p.pod)
	for _, pod := range p.list.Items {
		if err := controllerutil.SetOwnerReference(&pod, wl, c.Scheme()); err != nil {
			return nil, err
		}
	}

	return wl, nil
}

func (p *Pod) ListChildWorkloads(ctx context.Context, c client.Client, key types.NamespacedName) (*kueue.WorkloadList, error) {
	log := ctrl.LoggerFrom(ctx)

	workloads := &kueue.WorkloadList{}

	// Get related workloads for the pod group
	if p.isGroup {
		workload := &kueue.Workload{}
		if err := c.Get(ctx, types.NamespacedName{Name: key.Name, Namespace: key.Namespace}, workload); err != nil {
			if apierrors.IsNotFound(err) {
				return workloads, nil
			}
			log.Error(err, "Unable to get related workload for the pod group")
			return nil, err
		}

		workloads.Items = []kueue.Workload{*workload}
		return workloads, nil
	}

	// List related workloads for the single pod
	if err := c.List(ctx, workloads, client.InNamespace(key.Namespace),
		client.MatchingFields{jobframework.GetOwnerKey(gvk): key.Name}); err != nil {
		log.Error(err, "Unable to get related workload for the single pod")
		return nil, err
	}

	return workloads, nil
}

func (p *Pod) FindMatchingWorkloads(ctx context.Context, c client.Client) (*kueue.Workload, []*kueue.Workload, error) {
	log := ctrl.LoggerFrom(ctx)
	groupName := podGroupName(p.pod)

	if groupName == "" {
		return jobframework.FindMatchingWorkloads(ctx, c, p)
	}

	// Find a matching workload first if there is one.
	workload := &kueue.Workload{}
	if err := c.Get(ctx, types.NamespacedName{Name: groupName, Namespace: p.pod.GetNamespace()}, workload); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		log.Error(err, "Unable to get related workload")
		return nil, nil, err
	}

	// Cleanup excess pods for each workload pod set (role)
	activePods := p.runnableOrSucceededPods()
	for _, ps := range workload.Spec.PodSets {
		// Find all the active pods of the role
		var roleActivePods []corev1.Pod
		for _, activePod := range activePods {
			roleHash, err := getRoleHash(activePod)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to calculate pod role hash: %w", err)
			}

			if ps.Name == roleHash {
				roleActivePods = append(roleActivePods, activePod)
			}
		}

		// Cleanup excess pods of the role
		err := p.cleanupExcessPods(ctx, c, int(ps.Count), roleActivePods)
		if err != nil {
			return nil, nil, err
		}
	}

	jobPodSets, err := p.constructGroupPodSets()
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

	if wl.GetName() != podGroupName(p.pod) {
		return false
	}

	if !workloadFinished && len(wl.Spec.PodSets) < len(jobPodSets) {
		return false
	}

	// Match the current state of pod sets
	// to the pod set info in the workload
	j := -1
	for i := range jobPodSets {
		for j++; j < len(wl.Spec.PodSets); j++ {
			if jobPodSets[i].Name == wl.Spec.PodSets[j].Name {
				break
			}
		}
		// If actual pod set info has a role that workload doesn't have,
		// consider workload not an equivalent to the pod group
		if j == len(wl.Spec.PodSets) {
			return false
		}
		// Check counts for found pod sets
		if !workloadFinished && wl.Spec.PodSets[j].Count < jobPodSets[i].Count {
			return false
		}
	}

	return true
}

func (p *Pod) ReclaimablePods() ([]kueue.ReclaimablePod, error) {
	if !p.isGroup {
		return []kueue.ReclaimablePod{}, nil
	}

	var result []kueue.ReclaimablePod
	for _, pod := range p.list.Items {
		if pod.Status.Phase == corev1.PodSucceeded {
			roleHash, err := getRoleHash(pod)
			if err != nil {
				return nil, err
			}

			roleFound := false
			for i := range result {
				if result[i].Name == roleHash {
					result[i].Count++
					roleFound = true
				}
			}

			if !roleFound {
				result = append(result, kueue.ReclaimablePod{Name: roleHash, Count: 1})
			}
		}
	}

	return result, nil
}

func (p *Pod) finalizeNonRunnableNorSucceededPods(ctx context.Context, c client.Client) error {
	for _, p := range p.list.Items {
		if isPodRunnableOrSucceeded(&p) {
			continue
		}
		if controllerutil.RemoveFinalizer(&p, PodFinalizer) {
			if err := c.Update(ctx, &p); err != nil {
				return err
			}
		}
	}
	return nil
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
