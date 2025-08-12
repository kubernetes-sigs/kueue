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

package pod

import (
	"cmp"
	"context"
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
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	FrameworkName                  = "pod"
	ConditionTypeTerminationTarget = "TerminationTarget"
	errMsgIncorrectGroupRoleCount  = "pod group can't include more than 8 roles"
)

// Event reasons used by the pod controller
const (
	ReasonExcessPodDeleted     = "ExcessPodDeleted"
	ReasonOwnerReferencesAdded = "OwnerReferencesAdded"
)

const (
	// WorkloadWaitingForReplacementPods is True when Kueue doesn't observe all
	// the Pods declared for the group.
	WorkloadWaitingForReplacementPods = "WaitingForReplacementPods"

	// WorkloadPodsFailed means that at least one Pod are not runnable or not succeeded.
	WorkloadPodsFailed = "PodsFailed"
)

var (
	gvk                          = corev1.SchemeGroupVersion.WithKind("Pod")
	errIncorrectReconcileRequest = errors.New("event handler error: got a single pod reconcile request for a pod group")
	errPendingOps                = jobframework.UnretryableError("waiting to observe previous operations on pods")
	errPodGroupLabelsMismatch    = errors.New("constructing workload: pods have different label values")
	realClock                    = clock.RealClock{}
)

func init() {
	utilruntime.Must(jobframework.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:      SetupIndexes,
		NewJob:            NewJob,
		NewReconciler:     NewReconciler,
		SetupWebhook:      SetupWebhook,
		JobType:           &corev1.Pod{},
		MultiKueueAdapter: &multiKueueAdapter{},
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
	expectationsStore *expectations.Store
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return r.ReconcileGenericJob(ctx, req, NewPod(WithExcessPodExpectations(r.expectationsStore), WithClock(realClock)))
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	concurrency := mgr.GetControllerOptions().GroupKindConcurrency[gvk.GroupKind().String()]
	ctrl.Log.V(3).Info("Setting up Pod reconciler", "concurrency", max(1, concurrency))
	return ctrl.NewControllerManagedBy(mgr).
		Named("v1_pod").
		Watches(&corev1.Pod{}, &podEventHandler{cleanedUpPodsExpectations: r.expectationsStore}).
		Watches(&kueue.Workload{}, &workloadHandler{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: concurrency,
		}).
		Complete(r)
}

func NewJob() jobframework.GenericJob {
	return NewPod()
}

func NewReconciler(c client.Client, record record.EventRecorder, opts ...jobframework.Option) jobframework.JobReconcilerInterface {
	return &Reconciler{
		JobReconciler:     jobframework.NewReconciler(c, record, opts...),
		expectationsStore: expectations.NewStore("finalizedPods"),
	}
}

type Pod struct {
	pod                   corev1.Pod
	key                   types.NamespacedName
	isFound               bool
	isGroup               bool
	unretriableGroup      *bool
	list                  corev1.PodList
	absentPods            int
	excessPodExpectations *expectations.Store
	satisfiedExcessPods   bool
	clock                 clock.Clock
}

var (
	_ jobframework.GenericJob                      = (*Pod)(nil)
	_ jobframework.JobWithFinalize                 = (*Pod)(nil)
	_ jobframework.JobWithFinalize                 = (*Pod)(nil)
	_ jobframework.ComposableJob                   = (*Pod)(nil)
	_ jobframework.JobWithCustomWorkloadConditions = (*Pod)(nil)
	_ jobframework.TopLevelJob                     = (*Pod)(nil)
)

// PodOption is a function type that modifies a Pod. It allows customization of a Pod's
// properties during its creation.
type PodOption func(pod *Pod)

// WithExcessPodExpectations sets the excessPodExpectations field of the Pod to the given expectations store.
// This allows customizing the expectations for excess pods that are tracked by the Pod.
func WithExcessPodExpectations(store *expectations.Store) PodOption {
	return func(pod *Pod) {
		pod.excessPodExpectations = store
	}
}

// WithClock sets the clock field of the Pod to the given clock. This allows the Pod to
// use a custom clock instead of the default one.
func WithClock(clock clock.Clock) PodOption {
	return func(pod *Pod) {
		pod.clock = clock
	}
}

// NewPod creates a new Pod with the provided options. It applies all the given PodOptions
// to customize the Pod's fields. By default, the Pod is created with a real clock, unless
// overridden by a WithClock option.
func NewPod(opts ...PodOption) *Pod {
	pod := &Pod{
		clock: realClock, // Default clock is set to realClock.
	}
	for _, opt := range opts {
		opt(pod) // Apply each option to the pod.
	}
	return pod
}

func FromObject(o runtime.Object) *Pod {
	out := NewPod()
	out.pod = *o.(*corev1.Pod)
	return out
}

// Object returns the job instance.
func (p *Pod) Object() client.Object {
	return &p.pod
}

func podSuspended(p *corev1.Pod) bool {
	return utilpod.IsTerminated(p) || isGated(p)
}

func isUnretriablePod(pod corev1.Pod) bool {
	return pod.Annotations[podconstants.RetriableInGroupAnnotationKey] == podconstants.RetriableInGroupAnnotationValue
}

// isUnretriableGroup returns true if at least one pod in the group
// has a RetriableInGroupAnnotation set to 'false'.
func (p *Pod) isUnretriableGroup() bool {
	if p.unretriableGroup != nil {
		return *p.unretriableGroup
	}

	if slices.ContainsFunc(p.list.Items, isUnretriablePod) {
		p.unretriableGroup = ptr.To(true)
		return true
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

// Run will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) Run(ctx context.Context, c client.Client, podSetsInfo []podset.PodSetInfo, recorder record.EventRecorder, msg string) error {
	log := ctrl.LoggerFrom(ctx)

	if !p.isGroup {
		if len(podSetsInfo) != 1 {
			return fmt.Errorf("%w: expecting 1 pod set got %d", podset.ErrInvalidPodsetInfo, len(podSetsInfo))
		}

		if !isGated(&p.pod) {
			return nil
		}

		if err := clientutil.Patch(ctx, c, &p.pod, true, func() (bool, error) {
			return true, prepare(&p.pod, podSetsInfo[0])
		}); err != nil {
			return err
		}

		if recorder != nil {
			recorder.Event(&p.pod, corev1.EventTypeNormal, jobframework.ReasonStarted, msg)
		}

		return nil
	}

	return parallelize.Until(ctx, len(p.list.Items), func(i int) error {
		pod := &p.list.Items[i]

		if !isGated(pod) {
			return nil
		}

		if err := clientutil.Patch(ctx, c, pod, true, func() (bool, error) {
			roleHash, err := getRoleHash(*pod)
			if err != nil {
				return false, err
			}

			podSetIndex := slices.IndexFunc(podSetsInfo, func(info podset.PodSetInfo) bool {
				return string(info.Name) == roleHash
			})
			if podSetIndex == -1 {
				return false, fmt.Errorf("%w: podSetInfo with the name '%s' is not found", podset.ErrInvalidPodsetInfo, roleHash)
			}

			err = prepare(pod, podSetsInfo[podSetIndex])
			if err != nil {
				return false, err
			}

			log.V(3).Info("Starting pod in group", "podInGroup", klog.KObj(pod))

			return true, nil
		}); err != nil {
			return err
		}

		if recorder != nil {
			recorder.Event(pod, corev1.EventTypeNormal, jobframework.ReasonStarted, msg)
		}
		return nil
	})
}

func (p *Pod) IsTopLevel() bool {
	return true
}

// RunWithPodSetsInfo will inject the node affinity and podSet counts extracting from workload to job and unsuspend it.
func (p *Pod) RunWithPodSetsInfo(_ []podset.PodSetInfo) error {
	// Not implemented because this is not called when JobWithCustomRun is implemented.
	return errors.New("RunWithPodSetsInfo is not implemented for the Pod object")
}

// RestorePodSetsInfo will restore the original node affinity and podSet counts of the job.
func (p *Pod) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	// Not implemented since Pods cannot be updated, they can only be terminated.
	return false
}

// Finished means whether the job is completed/failed or not,
// condition represents the workload finished condition.
func (p *Pod) Finished() (message string, success, finished bool) {
	if p.isServing() {
		return "", true, false
	}

	finished = true
	success = true

	if !p.isGroup {
		if finished = utilpod.IsTerminated(&p.pod); finished {
			message = p.pod.Status.Message
			success = p.pod.Status.Phase == corev1.PodSucceeded
		}
		return message, success, finished
	}
	isActive := false
	succeededCount := 0

	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		ctrl.Log.V(2).Error(err, "failed to check if pod group is finished")
		message = "failed to check if pod group is finished"
		return message, success, false
	}
	for _, pod := range p.list.Items {
		if pod.Status.Phase == corev1.PodSucceeded {
			succeededCount++
		}

		if !utilpod.IsTerminated(&pod) {
			isActive = true
		}
	}

	unretriableGroup := p.isUnretriableGroup()

	if succeededCount == groupTotalCount || (!isActive && unretriableGroup) {
		message = fmt.Sprintf("Pods succeeded: %d/%d.", succeededCount, groupTotalCount)
	} else {
		return message, success, false
	}

	return message, success, finished
}

// PodSets will build workload podSets corresponding to the job.
func (p *Pod) PodSets() ([]kueue.PodSet, error) {
	if !p.isGroup {
		return constructPodSets(&p.pod)
	} else {
		return p.constructGroupPodSets()
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

func (p *Pod) PodLabelSelector() string {
	return fmt.Sprintf("%s=%s", podconstants.GroupNameLabel, p.pod.Labels[podconstants.GroupNameLabel])
}

func (p *Pod) Stop(ctx context.Context, c client.Client, _ []podset.PodSetInfo, stopReason jobframework.StopReason, eventMsg string) ([]client.Object, error) {
	var podsInGroup []corev1.Pod

	if p.isGroup {
		podsInGroup = p.list.Items
	} else {
		podsInGroup = []corev1.Pod{p.pod}
	}

	stoppedNow := make([]client.Object, 0)
	for i := range podsInGroup {
		// If the workload is being deleted, delete even finished Pods.
		if !podsInGroup[i].DeletionTimestamp.IsZero() || (stopReason != jobframework.StopReasonWorkloadDeleted && podSuspended(&podsInGroup[i])) {
			continue
		}
		podInGroup := FromObject(&podsInGroup[i])

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
							Time: p.clock.Now(),
						},
						Reason:  string(stopReason),
						Message: eventMsg,
					},
				},
			},
		}
		if err := c.Status().Patch(ctx, pCopy, client.Apply, client.FieldOwner(constants.KueueName)); err != nil && !apierrors.IsNotFound(err) {
			return stoppedNow, err
		}
		if err := c.Delete(ctx, podInGroup.Object()); err != nil && !apierrors.IsNotFound(err) {
			return stoppedNow, err
		}
		stoppedNow = append(stoppedNow, podInGroup.Object())
	}

	// If related workload is deleted, the generic reconciler will stop the pod group and finalize the workload.
	// However, it won't finalize the pods. Since the Stop method for the pod group deletes all the pods in the
	// group, the pods will be finalized here.
	if p.isGroup && stopReason == jobframework.StopReasonWorkloadDeleted {
		err := p.Finalize(ctx, c)
		if err != nil {
			return stoppedNow, err
		}
	}

	return stoppedNow, nil
}

func (p *Pod) ForEach(f func(obj runtime.Object)) {
	if p.isGroup {
		for _, pod := range p.list.Items {
			f(&pod)
		}
	} else {
		f(&p.pod)
	}
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &corev1.Pod{}, PodGroupNameCacheKey, IndexPodGroupName); err != nil {
		return err
	}
	if err := jobframework.SetupWorkloadOwnerIndex(ctx, indexer, gvk); err != nil {
		return err
	}
	return nil
}

func (p *Pod) Finalize(ctx context.Context, c client.Client) error {
	groupName := podGroupName(p.pod)

	var podsInGroup corev1.PodList
	if groupName == "" {
		podsInGroup.Items = append(podsInGroup.Items, *p.Object().(*corev1.Pod))
	} else {
		if err := c.List(ctx, &podsInGroup, client.MatchingFields{
			PodGroupNameCacheKey: groupName,
		}, client.InNamespace(p.pod.Namespace)); err != nil {
			return err
		}
	}

	return parallelize.Until(ctx, len(podsInGroup.Items), func(i int) error {
		pod := &podsInGroup.Items[i]
		return clientutil.Patch(ctx, c, pod, false, func() (bool, error) {
			return controllerutil.RemoveFinalizer(pod, podconstants.PodFinalizer), nil
		})
	})
}

func (p *Pod) Skip() bool {
	// Skip pod reconciliation, if pod is found, and it's managed label is not set or incorrect.
	if v, ok := p.pod.GetLabels()[constants.ManagedByKueueLabelKey]; p.isFound && (!ok || v != constants.ManagedByKueueLabelValue) {
		return true
	}
	return false
}

// podGroupName returns a value of GroupNameLabel for the pod object.
// Returns an empty string if there's no such label.
func podGroupName(p corev1.Pod) string {
	return p.GetLabels()[podconstants.GroupNameLabel]
}

// groupTotalCount returns the value of GroupTotalCountAnnotation for the pod being reconciled at the moment.
// It doesn't check if the whole group has the same total group count annotation value.
func (p *Pod) groupTotalCount() (int, error) {
	if podGroupName(p.pod) == "" {
		return 0, fmt.Errorf("pod doesn't have a '%s' label", podconstants.GroupNameLabel)
	}

	gtcAnnotation, ok := p.Object().GetAnnotations()[podconstants.GroupTotalCountAnnotation]
	if !ok {
		return 0, fmt.Errorf("failed to extract '%s' annotation",
			podconstants.GroupTotalCountAnnotation)
	}

	gtc, err := strconv.Atoi(gtcAnnotation)
	if err != nil {
		return 0, err
	}

	if gtc < 1 {
		return 0, fmt.Errorf("incorrect annotation value '%s=%s': group total count should be greater than zero",
			podconstants.GroupTotalCountAnnotation, gtcAnnotation)
	}

	return gtc, nil
}

// getRoleHash will filter all the fields of the pod that are relevant to admission (pod role) and return a sha256
// checksum of those fields. This is used to group the pods of the same roles when interacting with the workload.
func getRoleHash(p corev1.Pod) (string, error) {
	if roleHash, ok := p.Annotations[podconstants.RoleHashAnnotation]; ok {
		return roleHash, nil
	}
	return utilpod.GenerateRoleHash(&p.Spec)
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

	if err := c.List(ctx, &p.list, client.MatchingFields{
		PodGroupNameCacheKey: key.Name,
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
	if _, useFastAdmission := p.pod.GetAnnotations()[podconstants.GroupFastAdmissionAnnotationKey]; useFastAdmission {
		tc, err := p.groupTotalCount()
		if err != nil {
			return nil, err
		}
		return constructGroupPodSetsFast(p.list.Items, tc)
	}
	return constructGroupPodSets(p.list.Items)
}

func constructPodSets(p *corev1.Pod) ([]kueue.PodSet, error) {
	podSet, err := constructPodSet(p)
	if err != nil {
		return nil, err
	}
	return []kueue.PodSet{
		podSet,
	}, nil
}

func constructPodSet(p *corev1.Pod) (kueue.PodSet, error) {
	podSet := kueue.PodSet{
		Name:  kueue.DefaultPodSetName,
		Count: 1,
		Template: corev1.PodTemplateSpec{
			Spec: *p.Spec.DeepCopy(),
		},
	}
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(
			&p.ObjectMeta).PodIndexLabel(
			ptr.To(kueuealpha.PodGroupPodIndexLabel)).Build()
		if err != nil {
			return kueue.PodSet{}, err
		}
		podSet.TopologyRequest = topologyRequest
	}
	return podSet, nil
}

func constructGroupPodSetsFast(pods []corev1.Pod, groupTotalCount int) ([]kueue.PodSet, error) {
	for _, podInGroup := range pods {
		if !isPodRunnableOrSucceeded(&podInGroup) {
			continue
		}
		roleHash, err := getRoleHash(podInGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate pod role hash: %w", err)
		}
		podSets, err := constructPodSets(&podInGroup)
		if err != nil {
			return nil, err
		}
		podSets[0].Name = kueue.NewPodSetReference(roleHash)
		podSets[0].Count = int32(groupTotalCount)
		return podSets, nil
	}

	return nil, errors.New("failed to find a runnable pod in the group")
}

func constructGroupPodSets(pods []corev1.Pod) ([]kueue.PodSet, error) {
	var resultPodSets []kueue.PodSet

	for _, podInGroup := range pods {
		if !isPodRunnableOrSucceeded(&podInGroup) {
			continue
		}

		roleHash, err := getRoleHash(podInGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate pod role hash: %w", err)
		}

		podRoleFound := false
		for psi := range resultPodSets {
			if string(resultPodSets[psi].Name) == roleHash {
				podRoleFound = true
				resultPodSets[psi].Count++
				break
			}
		}

		if !podRoleFound {
			podSet, err := constructPodSet(&podInGroup)
			if err != nil {
				return nil, err
			}
			podSet.Name = kueue.NewPodSetReference(roleHash)

			resultPodSets = append(resultPodSets, podSet)
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

	_, err = utilpod.ReadUIntFromLabelBelowBound(p.Object(), kueuealpha.PodGroupPodIndexLabel, groupTotalCount)
	if utilpod.IgnoreLabelNotFoundError(err) != nil {
		return err
	}

	originalQueue := jobframework.QueueName(p)
	_, useFastAdmission := p.pod.GetAnnotations()[podconstants.GroupFastAdmissionAnnotationKey]

	if !useFastAdmission && len(activePods) < groupTotalCount {
		errMsg := fmt.Sprintf("'%s' group has fewer runnable pods than expected", podGroupName(p.pod))
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

		tc, err := strconv.Atoi(podInGroup.GetAnnotations()[podconstants.GroupTotalCountAnnotation])
		if err != nil {
			return fmt.Errorf("failed to extract '%s' annotation from the pod '%s': %w",
				podconstants.GroupTotalCountAnnotation,
				podInGroup.GetName(),
				err)
		}
		if tc != groupTotalCount {
			return jobframework.UnretryableError(fmt.Sprintf("pods '%s' and '%s' has different '%s' values: %d!=%d",
				p.pod.GetName(), podInGroup.GetName(),
				podconstants.GroupTotalCountAnnotation,
				groupTotalCount, tc))
		}
	}

	return nil
}

// runnableOrSucceededPods returns a slice of active pods in the group
func (p *Pod) runnableOrSucceededPods() []corev1.Pod {
	return utilslices.Pick(p.list.Items, isPodRunnableOrSucceeded)
}

// notRunnableNorSucceededPods returns a slice of inactive pods in the group
func (p *Pod) notRunnableNorSucceededPods() []corev1.Pod {
	return utilslices.Pick(p.list.Items, func(p *corev1.Pod) bool { return !isPodRunnableOrSucceeded(p) })
}

// isPodRunnableOrSucceeded returns whether the Pod can eventually run, is Running or Succeeded.
// A Pod cannot run if it's gated or has no node assignment while having a deletionTimestamp.
func isPodRunnableOrSucceeded(p *corev1.Pod) bool {
	if !p.DeletionTimestamp.IsZero() && len(p.Spec.NodeName) == 0 {
		return false
	}
	return p.Status.Phase != corev1.PodFailed
}

// lastActiveTime returns the last timestamp on which the pod was observed active:
// - the time the pod was declared Failed
// - the deletion time
func lastActiveTime(clock clock.Clock, p *corev1.Pod) time.Time {
	now := clock.Now()
	lastTransition := metav1.NewTime(now)
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.ContainersReady {
			if c.Status == corev1.ConditionFalse && c.Reason == string(corev1.PodFailed) {
				lastTransition = c.LastTransitionTime
			}
			break
		}
	}
	deletionTime := ptr.Deref(p.DeletionTimestamp, metav1.NewTime(now))
	if lastTransition.Before(&deletionTime) {
		return lastTransition.Time
	}
	return deletionTime.Time
}

// sortInactivePods sorts the provided pods slice based on:
// - finalizer state (pods with finalizers are first)
// - lastActiveTime (pods that were active last are first)
// - creation timestamp (newer pods are first)
func sortInactivePods(clock clock.Clock, inactivePods []corev1.Pod) {
	sort.Slice(inactivePods, func(i, j int) bool {
		pi := &inactivePods[i]
		pj := &inactivePods[j]
		iFin := slices.Contains(pi.Finalizers, podconstants.PodFinalizer)
		jFin := slices.Contains(pj.Finalizers, podconstants.PodFinalizer)
		if iFin != jFin {
			return iFin
		}

		iLastActive := lastActiveTime(clock, pi)
		jLastActive := lastActiveTime(clock, pj)

		if iLastActive.Equal(jLastActive) {
			return pi.CreationTimestamp.Before(&pj.CreationTimestamp)
		}
		return jLastActive.Before(iLastActive)
	})
}

// sortActivePods sorts the provided pods slice based on:
// - finalizer state (pods with no finalizers are last)
// - gated state (pods that are still gated are last)
// - creation timestamp (newer pods are last)
func sortActivePods(activePods []corev1.Pod) {
	// Sort active pods by creation timestamp
	sort.Slice(activePods, func(i, j int) bool {
		pi := &activePods[i]
		pj := &activePods[j]
		iFin := slices.Contains(pi.Finalizers, podconstants.PodFinalizer)
		jFin := slices.Contains(pj.Finalizers, podconstants.PodFinalizer)
		// Prefer to keep pods that have a finalizer.
		if iFin != jFin {
			return iFin
		}
		iGated := isGated(pi)
		jGated := isGated(pj)
		// Prefer to keep pods that aren't gated.
		if iGated != jGated {
			return !iGated
		}
		return pi.CreationTimestamp.Before(&pj.CreationTimestamp)
	})
}

func (p *Pod) removeExcessPods(ctx context.Context, c client.Client, r record.EventRecorder, extraPods []corev1.Pod) error {
	if len(extraPods) == 0 {
		return nil
	}

	log := ctrl.LoggerFrom(ctx)

	// Extract all the latest created extra pods
	extraPodsUIDs := utilslices.Map(extraPods, func(p *corev1.Pod) types.UID { return p.UID })
	p.excessPodExpectations.ExpectUIDs(log, p.key, extraPodsUIDs)

	// Finalize and delete the active pods created last
	err := parallelize.Until(ctx, len(extraPods), func(i int) error {
		pod := extraPods[i]
		if err := clientutil.Patch(ctx, c, &pod, false, func() (bool, error) {
			removed := controllerutil.RemoveFinalizer(&pod, podconstants.PodFinalizer)
			log.V(3).Info("Finalizing excess pod in group", "excessPod", klog.KObj(&pod))
			return removed, nil
		}); err != nil {
			// We won't observe this cleanup in the event handler.
			p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
			return err
		}
		if pod.DeletionTimestamp.IsZero() {
			log.V(3).Info("Deleting excess pod in group", "excessPod", klog.KObj(&pod))
			if err := c.Delete(ctx, &pod); err != nil {
				// We won't observe this cleanup in the event handler.
				p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
				return err
			}
			r.Event(&pod, corev1.EventTypeNormal, ReasonExcessPodDeleted, "Excess pod deleted")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (p *Pod) finalizePods(ctx context.Context, c client.Client, extraPods []corev1.Pod) error {
	if len(extraPods) == 0 {
		return nil
	}

	log := ctrl.LoggerFrom(ctx)

	// Extract all the latest created extra pods
	extraPodsUIDs := utilslices.Map(extraPods, func(p *corev1.Pod) types.UID { return p.UID })
	p.excessPodExpectations.ExpectUIDs(log, p.key, extraPodsUIDs)

	err := parallelize.Until(ctx, len(extraPods), func(i int) error {
		pod := extraPods[i]
		var removed bool
		if err := clientutil.Patch(ctx, c, &pod, false, func() (bool, error) {
			removed = controllerutil.RemoveFinalizer(&pod, podconstants.PodFinalizer)
			log.V(3).Info("Finalizing pod in group", "Pod", klog.KObj(&pod))
			return removed, nil
		}); err != nil {
			// We won't observe this cleanup in the event handler.
			p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
			return err
		}
		if !removed {
			// We don't expect an event in this case.
			p.excessPodExpectations.ObservedUID(log, p.key, pod.UID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (p *Pod) EnsureWorkloadOwnedByAllMembers(ctx context.Context, c client.Client, r record.EventRecorder, workload *kueue.Workload) error {
	if !p.isGroup {
		return jobframework.EnsurePrebuiltWorkloadOwnership(ctx, c, workload, &p.pod)
	}

	oldOwnersCnt := len(workload.GetOwnerReferences())
	for _, pod := range p.list.Items {
		if err := controllerutil.SetOwnerReference(&pod, workload, c.Scheme()); err != nil {
			return err
		}
	}
	newOwnersCnt := len(workload.GetOwnerReferences())
	if addedOwnersCnt := newOwnersCnt - oldOwnersCnt; addedOwnersCnt > 0 {
		log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(workload))
		log.V(4).Info("Adding owner references for workload", "count", addedOwnersCnt)
		err := c.Update(ctx, workload)
		if err == nil {
			r.Eventf(workload, corev1.EventTypeNormal, ReasonOwnerReferencesAdded, fmt.Sprintf("Added %d owner reference(s)", addedOwnersCnt))
		}
		return err
	}
	return nil
}

func (p *Pod) getWorkloadLabels(labelKeysToCopy []string) (map[string]string, error) {
	if len(labelKeysToCopy) == 0 {
		return nil, nil
	}
	if !p.isGroup {
		return utilmaps.FilterKeys(p.Object().GetLabels(), labelKeysToCopy), nil
	}
	workloadLabels := make(map[string]string, len(labelKeysToCopy))
	for _, pod := range p.list.Items {
		for _, labelKey := range labelKeysToCopy {
			labelValuePod, foundInPod := pod.Labels[labelKey]
			labelValueWorkload, foundInWorkload := workloadLabels[labelKey]
			if foundInPod && foundInWorkload && (labelValuePod != labelValueWorkload) {
				return nil, errPodGroupLabelsMismatch
			}
			if foundInPod {
				workloadLabels[labelKey] = labelValuePod
			}
		}
	}
	return workloadLabels, nil
}

func (p *Pod) ConstructComposableWorkload(ctx context.Context, c client.Client, r record.EventRecorder, labelKeysToCopy []string) (*kueue.Workload, error) {
	if !p.isGroup {
		return jobframework.ConstructWorkload(ctx, c, p, labelKeysToCopy)
	}

	if err := p.finalizePods(ctx, c, p.notRunnableNorSucceededPods()); err != nil {
		return nil, err
	}

	activePods := p.runnableOrSucceededPods()

	err := p.validatePodGroupMetadata(r, activePods)
	if err != nil {
		return nil, err
	}

	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		return nil, err
	}

	// Cleanup extra pods if there's any
	if excessPodsCount := len(activePods) - groupTotalCount; excessPodsCount > 0 {
		sortActivePods(activePods)
		err = p.removeExcessPods(ctx, c, r, activePods[len(activePods)-excessPodsCount:])
		if err != nil {
			return nil, err
		}
		p.list.Items = activePods[:len(activePods)-excessPodsCount]
	}

	podSets, err := p.PodSets()
	if err != nil {
		if jobframework.IsUnretryableError(err) {
			r.Eventf(p.Object(), corev1.EventTypeWarning, jobframework.ReasonErrWorkloadCompose, err.Error())
		}
		return nil, err
	}
	if len(podSets) > 8 {
		return nil, jobframework.UnretryableError(errMsgIncorrectGroupRoleCount)
	}

	wl := NewGroupWorkload(p.workloadName(), p.Object(), podSets, nil)

	for _, pod := range p.list.Items {
		if err := controllerutil.SetOwnerReference(&pod, wl, c.Scheme()); err != nil {
			return nil, err
		}
	}
	labelsToCopy, err := p.getWorkloadLabels(labelKeysToCopy)
	if err != nil {
		return nil, err
	}
	utilmaps.Copy(&wl.Labels, labelsToCopy)
	return wl, nil
}

func (p *Pod) workloadName() string {
	if prebuiltWorkloadName, usePrebuiltWorkload := jobframework.PrebuiltWorkloadFor(p); usePrebuiltWorkload {
		return prebuiltWorkloadName
	}

	if !p.isGroup {
		return GetWorkloadNameForPod(p.pod.GetName(), p.pod.GetUID())
	}

	return podGroupName(p.pod)
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
		client.MatchingFields{jobframework.OwnerReferenceIndexKey(gvk): key.Name}); err != nil {
		log.Error(err, "Unable to get related workload for the single pod")
		return nil, err
	}

	return workloads, nil
}

func (p *Pod) FindMatchingWorkloads(ctx context.Context, c client.Client, r record.EventRecorder) (*kueue.Workload, []*kueue.Workload, error) {
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

	defaultDuration := int32(-1)
	if ptr.Deref(workload.Spec.MaximumExecutionTimeSeconds, defaultDuration) != ptr.Deref(jobframework.MaximumExecutionTimeSeconds(p), defaultDuration) {
		return nil, []*kueue.Workload{workload}, nil
	}

	// Cleanup excess pods for each workload pod set (role)
	activePods := p.runnableOrSucceededPods()
	inactivePods := p.notRunnableNorSucceededPods()

	var absentPods int
	var keptPods []corev1.Pod
	var excessActivePods []corev1.Pod
	var replacedInactivePods []corev1.Pod

	for _, ps := range workload.Spec.PodSets {
		// Find all the active and inactive pods of the role
		var roleHashErrors []error
		hasRoleFunc := func(p *corev1.Pod) bool {
			hash, err := getRoleHash(*p)
			if err != nil {
				roleHashErrors = append(roleHashErrors, err)
				return false
			}
			return hash == string(ps.Name)
		}
		roleActivePods := utilslices.Pick(activePods, hasRoleFunc)
		roleInactivePods := utilslices.Pick(inactivePods, hasRoleFunc)
		if len(roleHashErrors) > 0 {
			return nil, nil, fmt.Errorf("failed to calculate pod role hash: %w", errors.Join(roleHashErrors...))
		}

		if absentCount := int(ps.Count) - len(roleActivePods); absentCount > 0 {
			absentPods += absentCount
		}

		if excessCount := len(roleActivePods) - int(ps.Count); excessCount > 0 {
			sortActivePods(roleActivePods)
			excessActivePods = append(excessActivePods, roleActivePods[len(roleActivePods)-excessCount:]...)
			keptPods = append(keptPods, roleActivePods[:len(roleActivePods)-excessCount]...)
		} else {
			keptPods = append(keptPods, roleActivePods...)
		}

		if finalizeablePodsCount := min(len(roleInactivePods), len(roleInactivePods)+len(roleActivePods)-int(ps.Count)); finalizeablePodsCount > 0 {
			sortInactivePods(p.clock, roleInactivePods)
			replacedInactivePods = append(replacedInactivePods, roleInactivePods[len(roleInactivePods)-finalizeablePodsCount:]...)
			keptPods = append(keptPods, roleInactivePods[:len(roleInactivePods)-finalizeablePodsCount]...)
		} else {
			keptPods = append(keptPods, roleInactivePods...)
		}
	}

	jobPodSets, err := constructGroupPodSets(keptPods)
	if err != nil {
		return nil, nil, err
	}

	if len(keptPods) == 0 || !p.equivalentToWorkload(workload, jobPodSets) {
		return nil, []*kueue.Workload{workload}, nil
	}

	// Do not clean up more pods until observing previous operations
	if !p.satisfiedExcessPods {
		return nil, nil, errPendingOps
	}

	p.absentPods = absentPods
	p.list.Items = keptPods
	if err := p.EnsureWorkloadOwnedByAllMembers(ctx, c, r, workload); err != nil {
		return nil, nil, err
	}

	if err := p.removeExcessPods(ctx, c, r, excessActivePods); err != nil {
		return nil, nil, err
	}

	if err := p.finalizePods(ctx, c, replacedInactivePods); err != nil {
		return nil, nil, err
	}
	return workload, []*kueue.Workload{}, nil
}

func (p *Pod) equivalentToWorkload(wl *kueue.Workload, jobPodSets []kueue.PodSet) bool {
	workloadFinished := apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)

	if wl.GetName() != p.workloadName() {
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

func (p *Pod) isServing() bool {
	return p.isGroup && p.pod.Annotations[podconstants.GroupServingAnnotationKey] == podconstants.GroupServingAnnotationValue
}

func (p *Pod) isReclaimable() bool {
	return p.isGroup && !p.isServing()
}

func (p *Pod) ReclaimablePods() ([]kueue.ReclaimablePod, error) {
	if !p.isReclaimable() {
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
				if string(result[i].Name) == roleHash {
					result[i].Count++
					roleFound = true
				}
			}

			if !roleFound {
				result = append(result, kueue.ReclaimablePod{Name: kueue.NewPodSetReference(roleHash), Count: 1})
			}
		}
	}

	return result, nil
}

func (p *Pod) CustomWorkloadConditions(wl *kueue.Workload) ([]metav1.Condition, bool) {
	var (
		conditions           []metav1.Condition
		needToUpdateWorkload bool
	)
	if condition, updated := p.waitingForReplacementPodsCondition(wl); condition != nil {
		conditions = append(conditions, *condition)
		if updated {
			needToUpdateWorkload = true
		}
	}
	return conditions, needToUpdateWorkload
}

func (p *Pod) waitingForReplacementPodsCondition(wl *kueue.Workload) (*metav1.Condition, bool) {
	if !p.isGroup {
		return nil, false
	}

	replCond := apimeta.FindStatusCondition(wl.Status.Conditions, WorkloadWaitingForReplacementPods)
	replCondStatus := p.absentPods > 0

	// Nothing to change.
	if replCond == nil && !replCondStatus {
		return nil, false
	}

	var updated bool

	if replCond == nil {
		replCond = &metav1.Condition{
			Type: WorkloadWaitingForReplacementPods,
		}
		updated = true
	}

	if replCondStatus {
		if evictCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted); evictCond != nil && evictCond.Status == metav1.ConditionTrue {
			if replCond.Reason != evictCond.Reason {
				replCond.Reason = evictCond.Reason
				updated = true
			}
			if replCond.Message != evictCond.Message {
				replCond.Message = evictCond.Message
				updated = true
			}
		} else if replCond.Status != metav1.ConditionTrue {
			replCond.Reason = WorkloadPodsFailed
			replCond.Message = "Some Failed pods need replacement"
			updated = true
		}

		if replCond.Status != metav1.ConditionTrue {
			replCond.Status = metav1.ConditionTrue
			updated = true
		}
	} else if replCond.Status != metav1.ConditionFalse {
		replCond.Status = metav1.ConditionFalse
		replCond.Reason = kueue.WorkloadPodsReady
		replCond.Message = "No pods need replacement"
		updated = true
	}

	if updated {
		replCond.ObservedGeneration = wl.Generation
		replCond.LastTransitionTime = metav1.NewTime(p.clock.Now())
	}

	return replCond, updated
}

func (p *Pod) EquivalentToWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) (bool, error) {
	// For single job using base EquivalentToWorkload method.
	if !p.isGroup {
		return jobframework.EquivalentToWorkload(ctx, c, p, wl)
	}

	podSets, err := p.constructGroupPodSets()
	if err != nil {
		return false, err
	}

	return p.equivalentToWorkload(wl, podSets), nil
}

func GetWorkloadNameForPod(podName string, podUID types.UID) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(podName, podUID, gvk)
}

func NewGroupWorkload(name string, obj client.Object, podSets []kueue.PodSet, labelKeysToCopy []string) *kueue.Workload {
	wl := jobframework.NewWorkload(name, obj, podSets, labelKeysToCopy)
	wl.Annotations[podconstants.IsGroupWorkloadAnnotationKey] = podconstants.IsGroupWorkloadAnnotationValue

	return wl
}

func isGated(pod *corev1.Pod) bool {
	return utilpod.HasGate(pod, podconstants.SchedulingGateName)
}

func prepare(pod *corev1.Pod, info podset.PodSetInfo) error {
	if err := podset.Merge(&pod.ObjectMeta, &pod.Spec, info); err != nil {
		return err
	}
	utilpod.Ungate(pod, podconstants.SchedulingGateName)
	// Remove the TopologySchedulingGate if the Pod is scheduled without using TAS
	if _, found := pod.Labels[kueuealpha.TASLabel]; !found {
		utilpod.Ungate(pod, kueuealpha.TopologySchedulingGate)
	}
	return nil
}

func gate(pod *corev1.Pod) bool {
	return utilpod.Gate(pod, podconstants.SchedulingGateName)
}
