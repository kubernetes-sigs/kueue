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

package v1beta2

import (
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// MakeDefaultOneLevelTopology creates a default topology with hostname level.
func MakeDefaultOneLevelTopology(name string) *kueue.Topology {
	return MakeTopology(name).
		Levels(corev1.LabelHostname).
		Obj()
}

// MakeDefaultTwoLevelTopology creates a default topology with block and rack levels.
func MakeDefaultTwoLevelTopology(name string) *kueue.Topology {
	return MakeTopology(name).
		Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel).
		Obj()
}

// MakeDefaultThreeLevelTopology creates a default topology with block, rack and hostname levels.
func MakeDefaultThreeLevelTopology(name string) *kueue.Topology {
	return MakeTopology(name).
		Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname).
		Obj()
}

type WorkloadWrapper struct{ kueue.Workload }

// MakeWorkload creates a wrapper for a Workload with a single pod
// with a single container.
func MakeWorkload(name, ns string) *WorkloadWrapper {
	return &WorkloadWrapper{kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{
				*MakePodSet(kueue.DefaultPodSetName, 1).Obj(),
			},
		},
	}}
}

// MakeWorkloadWithGeneratedName creates a wrapper for a Workload with a single pod
// with a single container.
func MakeWorkloadWithGeneratedName(namePrefix, ns string) *WorkloadWrapper {
	wl := MakeWorkload("", ns)
	wl.GenerateName = namePrefix
	return wl
}

func (w *WorkloadWrapper) Obj() *kueue.Workload {
	return &w.Workload
}

func (w *WorkloadWrapper) Clone() *WorkloadWrapper {
	return &WorkloadWrapper{Workload: *w.DeepCopy()}
}

func (w *WorkloadWrapper) UID(uid types.UID) *WorkloadWrapper {
	w.Workload.UID = uid
	return w
}

func (w *WorkloadWrapper) JobUID(uid string) *WorkloadWrapper {
	return w.Label(constants.JobUIDLabel, uid)
}

// Generation sets the generation of the Workload.
func (w *WorkloadWrapper) Generation(num int64) *WorkloadWrapper {
	w.ObjectMeta.Generation = num
	return w
}

func (w *WorkloadWrapper) Name(name string) *WorkloadWrapper {
	w.Workload.Name = name
	return w
}

func (w *WorkloadWrapper) Finalizers(fin ...string) *WorkloadWrapper {
	w.ObjectMeta.Finalizers = fin
	return w
}

func (w *WorkloadWrapper) Request(r corev1.ResourceName, q string) *WorkloadWrapper {
	w.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(q)
	return w
}

func (w *WorkloadWrapper) Limit(r corev1.ResourceName, q string) *WorkloadWrapper {
	res := &w.Spec.PodSets[0].Template.Spec.Containers[0].Resources
	if res.Limits == nil {
		res.Limits = corev1.ResourceList{
			r: resource.MustParse(q),
		}
	} else {
		res.Limits[r] = resource.MustParse(q)
	}
	return w
}

func (w *WorkloadWrapper) RequestAndLimit(r corev1.ResourceName, q string) *WorkloadWrapper {
	return w.Request(r, q).Limit(r, q)
}

func (w *WorkloadWrapper) Queue(q kueue.LocalQueueName) *WorkloadWrapper {
	w.Spec.QueueName = q
	return w
}

func (w *WorkloadWrapper) Active(a bool) *WorkloadWrapper {
	w.Spec.Active = ptr.To(a)
	return w
}

// SimpleReserveQuota reserves the quota for all the requested resources in one flavor.
// It assumes one podset with one container.
func (w *WorkloadWrapper) SimpleReserveQuota(cq, flavor string, now time.Time) *WorkloadWrapper {
	admission := MakeAdmission(cq, w.Spec.PodSets[0].Name)
	resReq := make(corev1.ResourceList)
	flavors := make(map[corev1.ResourceName]kueue.ResourceFlavorReference)
	for res, val := range w.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests {
		val.Mul(int64(w.Spec.PodSets[0].Count))
		resReq[res] = val
		flavors[res] = kueue.ResourceFlavorReference(flavor)
	}
	admission.PodSetAssignments[0].Count = ptr.To(w.Spec.PodSets[0].Count)
	admission.PodSetAssignments[0].Flavors = flavors
	admission.PodSetAssignments[0].ResourceUsage = resReq

	return w.ReserveQuotaAt(admission.Obj(), now)
}

// ReserveQuotaAt sets workload admission and adds a "QuotaReserved" status condition
func (w *WorkloadWrapper) ReserveQuotaAt(a *kueue.Admission, now time.Time) *WorkloadWrapper {
	w.Status.Admission = a
	w.Status.Conditions = []metav1.Condition{{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             "AdmittedByTest",
		Message:            fmt.Sprintf("Admitted by ClusterQueue %s", w.Status.Admission.ClusterQueue),
	}}
	return w
}

// QuotaReservedTime - sets the LastTransitionTime of the QuotaReserved condition if found.
func (w *WorkloadWrapper) QuotaReservedTime(t time.Time) *WorkloadWrapper {
	cond := apimeta.FindStatusCondition(w.Status.Conditions, kueue.WorkloadQuotaReserved)
	cond.LastTransitionTime = metav1.NewTime(t)
	return w
}

func (w *WorkloadWrapper) AdmittedAt(a bool, t time.Time) *WorkloadWrapper {
	cond := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
		Reason:             "ByTest",
		Message:            fmt.Sprintf("Admitted by ClusterQueue %s", w.Status.Admission.ClusterQueue),
	}
	if !a {
		cond.Status = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, cond)
	return w
}

func (w *WorkloadWrapper) Finished() *WorkloadWrapper {
	return w.FinishedAt(time.Now())
}

func (w *WorkloadWrapper) FinishedAt(t time.Time) *WorkloadWrapper {
	cond := metav1.Condition{
		Type:               kueue.WorkloadFinished,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
		Reason:             "ByTest",
		Message:            "Finished by test",
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, cond)
	return w
}

func (w *WorkloadWrapper) EvictedAt(t time.Time) *WorkloadWrapper {
	cond := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t),
		Reason:             "ByTest",
		Message:            "Evicted by test",
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, cond)
	return w
}

func (w *WorkloadWrapper) Creation(t time.Time) *WorkloadWrapper {
	w.CreationTimestamp = metav1.NewTime(t)
	return w
}

func (w *WorkloadWrapper) RuntimeClass(name string) *WorkloadWrapper {
	for i := range w.Spec.PodSets {
		w.Spec.PodSets[i].Template.Spec.RuntimeClassName = &name
	}
	return w
}

func (w *WorkloadWrapper) PriorityClassRef(ref *kueue.PriorityClassRef) *WorkloadWrapper {
	w.Spec.PriorityClassRef = ref
	return w
}

func (w *WorkloadWrapper) WorkloadPriorityClassRef(name string) *WorkloadWrapper {
	return w.PriorityClassRef(kueue.NewWorkloadPriorityClassRef(name))
}

func (w *WorkloadWrapper) PodPriorityClassRef(name string) *WorkloadWrapper {
	return w.PriorityClassRef(kueue.NewPodPriorityClassRef(name))
}

func (w *WorkloadWrapper) Priority(priority int32) *WorkloadWrapper {
	w.Spec.Priority = &priority
	return w
}

func (w *WorkloadWrapper) PodSets(podSets ...kueue.PodSet) *WorkloadWrapper {
	w.Spec.PodSets = podSets
	return w
}

func (w *WorkloadWrapper) Toleration(t corev1.Toleration) *WorkloadWrapper {
	w.Spec.PodSets[0].Template.Spec.Tolerations = append(w.Spec.PodSets[0].Template.Spec.Tolerations, t)
	return w
}

func (w *WorkloadWrapper) NodeSelector(kv map[string]string) *WorkloadWrapper {
	w.Spec.PodSets[0].Template.Spec.NodeSelector = kv
	return w
}

func (w *WorkloadWrapper) Condition(condition metav1.Condition) *WorkloadWrapper {
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
	return w
}

func (w *WorkloadWrapper) AdmissionCheck(ac kueue.AdmissionCheckState) *WorkloadWrapper {
	w.Status.AdmissionChecks = append(w.Status.AdmissionChecks, ac)
	return w
}

func (w *WorkloadWrapper) ResourceRequests(rr ...kueue.PodSetRequest) *WorkloadWrapper {
	w.Status.ResourceRequests = rr
	return w
}

func (w *WorkloadWrapper) ReclaimablePods(rps ...kueue.ReclaimablePod) *WorkloadWrapper {
	w.Status.ReclaimablePods = rps
	return w
}

func (w *WorkloadWrapper) Labels(l map[string]string) *WorkloadWrapper {
	w.ObjectMeta.Labels = l
	return w
}

func (w *WorkloadWrapper) Label(k, v string) *WorkloadWrapper {
	if w.ObjectMeta.Labels == nil {
		w.ObjectMeta.Labels = make(map[string]string)
	}
	w.ObjectMeta.Labels[k] = v
	return w
}

func (w *WorkloadWrapper) Annotation(k, v string) *WorkloadWrapper {
	if w.ObjectMeta.Annotations == nil {
		w.ObjectMeta.Annotations = make(map[string]string)
	}
	w.ObjectMeta.Annotations[k] = v
	return w
}

func (w *WorkloadWrapper) AdmissionChecks(checks ...kueue.AdmissionCheckState) *WorkloadWrapper {
	w.Status.AdmissionChecks = checks
	return w
}

func (w *WorkloadWrapper) Admission(admission *kueue.Admission) *WorkloadWrapper {
	w.Status.Admission = admission
	return w
}

func (w *WorkloadWrapper) Conditions(conditions ...metav1.Condition) *WorkloadWrapper {
	w.Status.Conditions = conditions
	return w
}

func (w *WorkloadWrapper) ControllerReference(gvk schema.GroupVersionKind, name, uid string) *WorkloadWrapper {
	AppendOwnerReference(&w.Workload, gvk, name, uid, ptr.To(true), ptr.To(true))
	return w
}

func (w *WorkloadWrapper) OwnerReference(gvk schema.GroupVersionKind, name, uid string) *WorkloadWrapper {
	AppendOwnerReference(&w.Workload, gvk, name, uid, nil, nil)
	return w
}

func (w *WorkloadWrapper) Annotations(annotations map[string]string) *WorkloadWrapper {
	w.ObjectMeta.Annotations = annotations
	return w
}

func (w *WorkloadWrapper) UnhealthyNodes(nodeNames ...string) *WorkloadWrapper {
	for _, nodeName := range nodeNames {
		w.Status.UnhealthyNodes = append(w.Status.UnhealthyNodes, kueue.UnhealthyNode{Name: nodeName})
	}
	return w
}

// DeletionTimestamp sets a deletion timestamp for the workload.
func (w *WorkloadWrapper) DeletionTimestamp(t time.Time) *WorkloadWrapper {
	w.Workload.DeletionTimestamp = ptr.To(metav1.NewTime(t).Rfc3339Copy())
	return w
}

func (w *WorkloadWrapper) RequeueState(count *int32, requeueAt *metav1.Time) *WorkloadWrapper {
	if count == nil && requeueAt == nil {
		w.Status.RequeueState = nil
		return w
	}
	if w.Status.RequeueState == nil {
		w.Status.RequeueState = &kueue.RequeueState{}
	}
	if count != nil {
		w.Status.RequeueState.Count = count
	}
	if requeueAt != nil {
		w.Status.RequeueState.RequeueAt = requeueAt
	}
	return w
}

func (w *WorkloadWrapper) ResourceVersion(v string) *WorkloadWrapper {
	w.SetResourceVersion(v)
	return w
}

func (w *WorkloadWrapper) MaximumExecutionTimeSeconds(v int32) *WorkloadWrapper {
	w.Spec.MaximumExecutionTimeSeconds = &v
	return w
}

func (w *WorkloadWrapper) PastAdmittedTime(v int32) *WorkloadWrapper {
	w.Status.AccumulatedPastExecutionTimeSeconds = &v
	return w
}

func (w *WorkloadWrapper) SchedulingStatsEviction(evictionState kueue.WorkloadSchedulingStatsEviction) *WorkloadWrapper {
	if w.Status.SchedulingStats == nil {
		w.Status.SchedulingStats = &kueue.SchedulingStats{}
	}
	w.Status.SchedulingStats.Evictions = append(w.Status.SchedulingStats.Evictions, evictionState)
	return w
}

func (w *WorkloadWrapper) ClusterName(clusterName string) *WorkloadWrapper {
	w.Status.ClusterName = &clusterName
	return w
}

func (w *WorkloadWrapper) NominatedClusterNames(nominatedClusterNames ...string) *WorkloadWrapper {
	w.Status.NominatedClusterNames = nominatedClusterNames
	return w
}

func AppendOwnerReference(obj client.Object, gvk schema.GroupVersionKind, name, uid string, controller, blockDeletion *bool) {
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               name,
		UID:                types.UID(uid),
		Controller:         controller,
		BlockOwnerDeletion: blockDeletion,
	}))
}

type PodSetWrapper struct{ kueue.PodSet }

func MakePodSet(name kueue.PodSetReference, count int) *PodSetWrapper {
	return &PodSetWrapper{
		kueue.PodSet{
			Name:  name,
			Count: int32(count),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name: "c",
							Resources: corev1.ResourceRequirements{
								Requests: make(corev1.ResourceList),
							},
						},
					},
				},
			},
		},
	}
}

func (p *PodSetWrapper) PodSpec(ps corev1.PodSpec) *PodSetWrapper {
	p.Template.Spec = ps
	return p
}

func (p *PodSetWrapper) PriorityClass(pc string) *PodSetWrapper {
	p.Template.Spec.PriorityClassName = pc
	return p
}

func (p *PodSetWrapper) RuntimeClass(name string) *PodSetWrapper {
	p.Template.Spec.RuntimeClassName = &name
	return p
}

func (p *PodSetWrapper) RestartPolicy(policy corev1.RestartPolicy) *PodSetWrapper {
	p.Template.Spec.RestartPolicy = policy
	return p
}

func (p *PodSetWrapper) RequiredTopologyRequest(level string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.Required = &level
	return p
}

func (p *PodSetWrapper) PodSetGroup(name string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.PodSetGroupName = &name
	return p
}

func (p *PodSetWrapper) SliceRequiredTopologyRequest(level string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.PodSetSliceRequiredTopology = &level
	return p
}

func (p *PodSetWrapper) SliceSizeTopologyRequest(size int32) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.PodSetSliceSize = &size
	return p
}

func (p *PodSetWrapper) PreferredTopologyRequest(level string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.Preferred = &level
	return p
}

func (p *PodSetWrapper) UnconstrainedTopologyRequest() *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.Unconstrained = ptr.To(true)
	return p
}

func (p *PodSetWrapper) PodIndexLabel(label *string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.PodIndexLabel = label
	return p
}

func (p *PodSetWrapper) SubGroupIndexLabel(label *string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.SubGroupIndexLabel = label
	return p
}

func (p *PodSetWrapper) SubGroupCount(count *int32) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.SubGroupCount = count
	return p
}

func (p *PodSetWrapper) Obj() *kueue.PodSet {
	return &p.PodSet
}

func (p *PodSetWrapper) Clone() *PodSetWrapper {
	return &PodSetWrapper{PodSet: *p.DeepCopy()}
}

func (p *PodSetWrapper) Request(r corev1.ResourceName, q string) *PodSetWrapper {
	p.Template.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(q)
	return p
}

func (p *PodSetWrapper) Limit(r corev1.ResourceName, q string) *PodSetWrapper {
	if p.Template.Spec.Containers[0].Resources.Limits == nil {
		p.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{}
	}
	p.Template.Spec.Containers[0].Resources.Limits[r] = resource.MustParse(q)
	return p
}

func (p *PodSetWrapper) Image(image string) *PodSetWrapper {
	p.Template.Spec.Containers[0].Image = image
	return p
}

func (p *PodSetWrapper) SetMinimumCount(mc int32) *PodSetWrapper {
	p.MinCount = &mc
	return p
}

func (p *PodSetWrapper) Toleration(t corev1.Toleration) *PodSetWrapper {
	p.Template.Spec.Tolerations = append(p.Template.Spec.Tolerations, t)
	return p
}

func (p *PodSetWrapper) Containers(containers ...corev1.Container) *PodSetWrapper {
	p.Template.Spec.Containers = containers
	return p
}

func (p *PodSetWrapper) InitContainers(containers ...corev1.Container) *PodSetWrapper {
	p.Template.Spec.InitContainers = containers
	return p
}

func (p *PodSetWrapper) NodeSelector(kv map[string]string) *PodSetWrapper {
	p.Template.Spec.NodeSelector = kv
	return p
}

func (p *PodSetWrapper) RequiredDuringSchedulingIgnoredDuringExecution(nodeSelectorTerms []corev1.NodeSelectorTerm) *PodSetWrapper {
	if p.Template.Spec.Affinity == nil {
		p.Template.Spec.Affinity = &corev1.Affinity{}
	}
	if p.Template.Spec.Affinity.NodeAffinity == nil {
		p.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	if p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
		p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
		nodeSelectorTerms...,
	)
	return p
}

func (p *PodSetWrapper) NodeName(name string) *PodSetWrapper {
	p.Template.Spec.NodeName = name
	return p
}

func (p *PodSetWrapper) Labels(kv map[string]string) *PodSetWrapper {
	p.Template.Labels = kv
	return p
}

func (p *PodSetWrapper) Annotations(kv map[string]string) *PodSetWrapper {
	p.Template.Annotations = kv
	return p
}

func (p *PodSetWrapper) SchedulingGates(sg ...corev1.PodSchedulingGate) *PodSetWrapper {
	p.Template.Spec.SchedulingGates = sg
	return p
}

func (p *PodSetWrapper) PodOverHead(resources corev1.ResourceList) *PodSetWrapper {
	p.Template.Spec.Overhead = resources
	return p
}

func (p *PodSetWrapper) ResourceClaimTemplate(claimName, templateName string) *PodSetWrapper {
	p.Template.Spec.ResourceClaims = append(p.Template.Spec.ResourceClaims, corev1.PodResourceClaim{
		Name:                      claimName,
		ResourceClaimTemplateName: ptr.To(templateName),
	})
	if len(p.Template.Spec.Containers) > 0 {
		p.Template.Spec.Containers[0].Resources.Claims = append(
			p.Template.Spec.Containers[0].Resources.Claims,
			corev1.ResourceClaim{Name: claimName},
		)
	}
	return p
}

func (p *PodSetWrapper) ResourceClaim(claimName, resourceClaimName string) *PodSetWrapper {
	p.Template.Spec.ResourceClaims = append(p.Template.Spec.ResourceClaims, corev1.PodResourceClaim{
		Name:              claimName,
		ResourceClaimName: ptr.To(resourceClaimName),
	})
	if len(p.Template.Spec.Containers) > 0 {
		p.Template.Spec.Containers[0].Resources.Claims = append(
			p.Template.Spec.Containers[0].Resources.Claims,
			corev1.ResourceClaim{Name: claimName},
		)
	}
	return p
}

// AdmissionWrapper wraps an Admission
type AdmissionWrapper struct{ kueue.Admission }

func MakeAdmission(cq string, podSetNames ...kueue.PodSetReference) *AdmissionWrapper {
	wrap := &AdmissionWrapper{kueue.Admission{
		ClusterQueue: kueue.ClusterQueueReference(cq),
	}}

	if len(podSetNames) == 0 {
		wrap.PodSetAssignments = []kueue.PodSetAssignment{
			MakePodSetAssignment(kueue.DefaultPodSetName).Obj(),
		}
		return wrap
	}

	var psFlavors []kueue.PodSetAssignment
	for _, name := range podSetNames {
		psFlavors = append(psFlavors, MakePodSetAssignment(name).Obj())
	}
	wrap.PodSetAssignments = psFlavors
	return wrap
}

func (w *AdmissionWrapper) Obj() *kueue.Admission {
	return &w.Admission
}

func (w *AdmissionWrapper) PodSets(podSets ...kueue.PodSetAssignment) *AdmissionWrapper {
	w.PodSetAssignments = podSets
	return w
}

// LocalQueueWrapper wraps a Queue.
type LocalQueueWrapper struct{ kueue.LocalQueue }

// MakeLocalQueue creates a wrapper for a LocalQueue.
func MakeLocalQueue(name, ns string) *LocalQueueWrapper {
	return &LocalQueueWrapper{kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}}
}

// Creation sets the creation timestamp of the LocalQueue.
func (q *LocalQueueWrapper) Creation(t time.Time) *LocalQueueWrapper {
	q.CreationTimestamp = metav1.NewTime(t)
	return q
}

// Label sets the label on the LocalQueue.
func (q *LocalQueueWrapper) Label(k, v string) *LocalQueueWrapper {
	if q.Labels == nil {
		q.Labels = make(map[string]string)
	}
	q.Labels[k] = v
	return q
}

// Obj returns the inner LocalQueue.
func (q *LocalQueueWrapper) Obj() *kueue.LocalQueue {
	return &q.LocalQueue
}

// ClusterQueue updates the clusterQueue the queue points to.
func (q *LocalQueueWrapper) ClusterQueue(c string) *LocalQueueWrapper {
	q.Spec.ClusterQueue = kueue.ClusterQueueReference(c)
	return q
}

// StopPolicy sets the stop policy.
func (q *LocalQueueWrapper) StopPolicy(p kueue.StopPolicy) *LocalQueueWrapper {
	q.Spec.StopPolicy = &p
	return q
}

// FairSharing sets the fair sharing config.
func (q *LocalQueueWrapper) FairSharing(fs *kueue.FairSharing) *LocalQueueWrapper {
	q.Spec.FairSharing = fs
	return q
}

// PendingWorkloads updates the pendingWorkloads in status.
func (q *LocalQueueWrapper) PendingWorkloads(n int32) *LocalQueueWrapper {
	q.Status.PendingWorkloads = n
	return q
}

// ReservingWorkloads updates the reservingWorkloads in status.
func (q *LocalQueueWrapper) ReservingWorkloads(n int32) *LocalQueueWrapper {
	q.Status.ReservingWorkloads = n
	return q
}

// AdmittedWorkloads updates the admittedWorkloads in status.
func (q *LocalQueueWrapper) AdmittedWorkloads(n int32) *LocalQueueWrapper {
	q.Status.AdmittedWorkloads = n
	return q
}

// Condition sets a condition on the LocalQueue.
func (q *LocalQueueWrapper) Condition(conditionType string, status metav1.ConditionStatus, reason, message string, generation int64) *LocalQueueWrapper {
	apimeta.SetStatusCondition(&q.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
	return q
}

func (q *LocalQueueWrapper) Active(status metav1.ConditionStatus) *LocalQueueWrapper {
	apimeta.SetStatusCondition(&q.Status.Conditions, metav1.Condition{
		Type:    kueue.LocalQueueActive,
		Status:  status,
		Reason:  "Ready",
		Message: "Can submit new workloads to localQueue",
	})
	return q
}

// FairSharingStatus updates the fairSharing in status.
func (q *LocalQueueWrapper) FairSharingStatus(status *kueue.LocalQueueFairSharingStatus) *LocalQueueWrapper {
	q.Status.FairSharing = status
	return q
}

// Generation sets the generation of the LocalQueue.
func (q *LocalQueueWrapper) Generation(num int64) *LocalQueueWrapper {
	q.ObjectMeta.Generation = num
	return q
}

// GeneratedName sets the prefix for the server to generate unique name.
// No name should be given in the MakeClusterQueue for the GeneratedName to work.
func (q *LocalQueueWrapper) GeneratedName(name string) *LocalQueueWrapper {
	q.GenerateName = name
	return q
}

type CohortWrapper struct {
	kueue.Cohort
}

func MakeCohort(name kueue.CohortReference) *CohortWrapper {
	return &CohortWrapper{kueue.Cohort{
		ObjectMeta: metav1.ObjectMeta{
			Name: string(name),
		},
	}}
}

func (c *CohortWrapper) Obj() *kueue.Cohort {
	return &c.Cohort
}

func (c *CohortWrapper) Parent(parentName kueue.CohortReference) *CohortWrapper {
	c.Spec.ParentName = parentName
	return c
}

// ResourceGroup adds a ResourceGroup with flavors.
func (c *CohortWrapper) ResourceGroup(flavors ...kueue.FlavorQuotas) *CohortWrapper {
	c.Spec.ResourceGroups = append(c.Spec.ResourceGroups, ResourceGroup(flavors...))
	return c
}

func (c *CohortWrapper) FairWeight(w resource.Quantity) *CohortWrapper {
	if c.Spec.FairSharing == nil {
		c.Spec.FairSharing = &kueue.FairSharing{}
	}
	c.Spec.FairSharing.Weight = &w
	return c
}

// ClusterQueueWrapper wraps a ClusterQueue.
type ClusterQueueWrapper struct{ kueue.ClusterQueue }

// MakeClusterQueue creates a wrapper for a ClusterQueue with a
// select-all NamespaceSelector.
func MakeClusterQueue(name string) *ClusterQueueWrapper {
	return &ClusterQueueWrapper{kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kueue.ClusterQueueSpec{
			NamespaceSelector: &metav1.LabelSelector{},
			QueueingStrategy:  kueue.BestEffortFIFO,
			FlavorFungibility: &kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.TryNextFlavor,
			},
		},
	}}
}

func (c *ClusterQueueWrapper) Clone() *ClusterQueueWrapper {
	return &ClusterQueueWrapper{ClusterQueue: *c.DeepCopy()}
}

// Obj returns the inner ClusterQueue.
func (c *ClusterQueueWrapper) Obj() *kueue.ClusterQueue {
	return &c.ClusterQueue
}

// Cohort sets the borrowing cohort.
func (c *ClusterQueueWrapper) Cohort(cohort kueue.CohortReference) *ClusterQueueWrapper {
	c.Spec.CohortName = cohort
	return c
}

func (c *ClusterQueueWrapper) AdmissionCheckStrategy(acs ...kueue.AdmissionCheckStrategyRule) *ClusterQueueWrapper {
	if c.Spec.AdmissionChecksStrategy == nil {
		c.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{}
	}
	c.Spec.AdmissionChecksStrategy.AdmissionChecks = acs
	return c
}

func (c *ClusterQueueWrapper) AdmissionMode(am kueue.AdmissionMode) *ClusterQueueWrapper {
	if c.Spec.AdmissionScope == nil {
		c.Spec.AdmissionScope = &kueue.AdmissionScope{}
	}
	c.Spec.AdmissionScope.AdmissionMode = am
	return c
}

func (c *ClusterQueueWrapper) Active(status metav1.ConditionStatus) *ClusterQueueWrapper {
	apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:    kueue.ClusterQueueActive,
		Status:  status,
		Reason:  "By test",
		Message: "by test",
	})
	return c
}

// GeneratedName sets the prefix for the server to generate unique name.
// No name should be given in the MakeClusterQueue for the GeneratedName to work.
func (c *ClusterQueueWrapper) GeneratedName(name string) *ClusterQueueWrapper {
	c.GenerateName = name
	return c
}

// ResourceGroup creates a ResourceGroup with the given FlavorQuotas.
func ResourceGroup(flavors ...kueue.FlavorQuotas) kueue.ResourceGroup {
	rg := kueue.ResourceGroup{
		Flavors: flavors,
	}
	if len(flavors) > 0 {
		var resources []corev1.ResourceName
		for _, r := range flavors[0].Resources {
			resources = append(resources, r.Name)
		}
		for i := 1; i < len(flavors); i++ {
			if len(flavors[i].Resources) != len(resources) {
				panic("Must list the same resources in all flavors in a ResourceGroup")
			}
			for j, r := range flavors[i].Resources {
				if r.Name != resources[j] {
					panic("Must list the same resources in all flavors in a ResourceGroup")
				}
			}
		}
		rg.CoveredResources = resources
	}
	return rg
}

// ResourceGroup adds a ResourceGroup with flavors.
func (c *ClusterQueueWrapper) ResourceGroup(flavors ...kueue.FlavorQuotas) *ClusterQueueWrapper {
	c.Spec.ResourceGroups = append(c.Spec.ResourceGroups, ResourceGroup(flavors...))
	return c
}

// AdmissionChecks replaces the queue additional checks.
// This is a convenience wrapper that converts to the AdmissionChecksStrategy format.
func (c *ClusterQueueWrapper) AdmissionChecks(checks ...kueue.AdmissionCheckReference) *ClusterQueueWrapper {
	// Convert simple admission check references to the strategy format
	acs := make([]kueue.AdmissionCheckStrategyRule, len(checks))
	for i, check := range checks {
		acs[i] = kueue.AdmissionCheckStrategyRule{
			Name: check,
		}
	}
	c.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
		AdmissionChecks: acs,
	}
	return c
}

// QueueingStrategy sets the queueing strategy in this ClusterQueue.
func (c *ClusterQueueWrapper) QueueingStrategy(strategy kueue.QueueingStrategy) *ClusterQueueWrapper {
	c.Spec.QueueingStrategy = strategy
	return c
}

// NamespaceSelector sets the namespace selector.
func (c *ClusterQueueWrapper) NamespaceSelector(s *metav1.LabelSelector) *ClusterQueueWrapper {
	c.Spec.NamespaceSelector = s
	return c
}

// Preemption sets the preemption policies.
func (c *ClusterQueueWrapper) Preemption(p kueue.ClusterQueuePreemption) *ClusterQueueWrapper {
	c.Spec.Preemption = &p
	return c
}

// FlavorFungibility sets the flavorFungibility policies.
func (c *ClusterQueueWrapper) FlavorFungibility(p kueue.FlavorFungibility) *ClusterQueueWrapper {
	c.Spec.FlavorFungibility = &p
	return c
}

// StopPolicy sets the stop policy.
func (c *ClusterQueueWrapper) StopPolicy(p kueue.StopPolicy) *ClusterQueueWrapper {
	c.Spec.StopPolicy = &p
	return c
}

// DeletionTimestamp sets a deletion timestamp for the cluster queue.
func (c *ClusterQueueWrapper) DeletionTimestamp(t time.Time) *ClusterQueueWrapper {
	c.ClusterQueue.DeletionTimestamp = ptr.To(metav1.NewTime(t).Rfc3339Copy())
	return c
}

func (c *ClusterQueueWrapper) Label(k, v string) *ClusterQueueWrapper {
	if c.Labels == nil {
		c.Labels = make(map[string]string)
	}
	c.Labels[k] = v
	return c
}

func (c *ClusterQueueWrapper) FairWeight(w resource.Quantity) *ClusterQueueWrapper {
	if c.Spec.FairSharing == nil {
		c.Spec.FairSharing = &kueue.FairSharing{}
	}
	c.Spec.FairSharing.Weight = &w
	return c
}

// Condition sets a condition on the ClusterQueue.
func (c *ClusterQueueWrapper) Condition(conditionType string, status metav1.ConditionStatus, reason, message string) *ClusterQueueWrapper {
	apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
	return c
}

// Generation sets the generation of the ClusterQueue.
func (c *ClusterQueueWrapper) Generation(num int64) *ClusterQueueWrapper {
	c.ObjectMeta.Generation = num
	return c
}

// Creation sets the creation timestamp of the ClusterQueue.
func (c *ClusterQueueWrapper) Creation(t time.Time) *ClusterQueueWrapper {
	c.CreationTimestamp = metav1.NewTime(t)
	return c
}

// PendingWorkloads sets the pendingWorkloads in status.
func (c *ClusterQueueWrapper) PendingWorkloads(n int32) *ClusterQueueWrapper {
	c.Status.PendingWorkloads = n
	return c
}

// AdmittedWorkloads sets the admittedWorkloads in status.
func (c *ClusterQueueWrapper) AdmittedWorkloads(n int32) *ClusterQueueWrapper {
	c.Status.AdmittedWorkloads = n
	return c
}

// FlavorQuotasWrapper wraps a FlavorQuotas object.
type FlavorQuotasWrapper struct{ kueue.FlavorQuotas }

// MakeFlavorQuotas creates a wrapper for a resource flavor.
func MakeFlavorQuotas(name string) *FlavorQuotasWrapper {
	return &FlavorQuotasWrapper{kueue.FlavorQuotas{
		Name: kueue.ResourceFlavorReference(name),
	}}
}

// Obj returns the inner flavor.
func (f *FlavorQuotasWrapper) Obj() *kueue.FlavorQuotas {
	return &f.FlavorQuotas
}

// Resource takes ResourceName, followed by the optional NominalQuota, BorrowingLimit, LendingLimit.
func (f *FlavorQuotasWrapper) Resource(name corev1.ResourceName, qs ...string) *FlavorQuotasWrapper {
	resourceWrapper := f.ResourceQuotaWrapper(name)
	if len(qs) > 0 {
		resourceWrapper.NominalQuota(qs[0])
	}
	if len(qs) > 1 && len(qs[1]) > 0 {
		resourceWrapper.BorrowingLimit(qs[1])
	}
	if len(qs) > 2 && len(qs[2]) > 0 {
		resourceWrapper.LendingLimit(qs[2])
	}
	if len(qs) > 3 {
		panic("Must have at most 3 quantities for nominalQuota, borrowingLimit and lendingLimit")
	}
	return resourceWrapper.Append()
}

// ResourceQuotaWrapper allows creation the creation of a Resource in a type-safe manner.
func (f *FlavorQuotasWrapper) ResourceQuotaWrapper(name corev1.ResourceName) *ResourceQuotaWrapper {
	rq := kueue.ResourceQuota{
		Name: name,
	}
	return &ResourceQuotaWrapper{parent: f, ResourceQuota: rq}
}

// ResourceQuotaWrapper wraps a ResourceQuota object.
type ResourceQuotaWrapper struct {
	parent *FlavorQuotasWrapper
	kueue.ResourceQuota
}

func (rq *ResourceQuotaWrapper) NominalQuota(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.NominalQuota = resource.MustParse(quantity)
	return rq
}

func (rq *ResourceQuotaWrapper) BorrowingLimit(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.BorrowingLimit = ptr.To(resource.MustParse(quantity))
	return rq
}

func (rq *ResourceQuotaWrapper) LendingLimit(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.LendingLimit = ptr.To(resource.MustParse(quantity))
	return rq
}

// Append appends the ResourceQuotaWrapper to its parent
func (rq *ResourceQuotaWrapper) Append() *FlavorQuotasWrapper {
	rq.parent.Resources = append(rq.parent.Resources, rq.ResourceQuota)
	return rq.parent
}

// ResourceFlavorWrapper wraps a ResourceFlavor.
type ResourceFlavorWrapper struct{ kueue.ResourceFlavor }

// MakeResourceFlavor creates a wrapper for a ResourceFlavor.
func MakeResourceFlavor(name string) *ResourceFlavorWrapper {
	return &ResourceFlavorWrapper{kueue.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kueue.ResourceFlavorSpec{
			NodeLabels: make(map[string]string),
		},
	}}
}

// Obj returns the inner ResourceFlavor.
func (rf *ResourceFlavorWrapper) Obj() *kueue.ResourceFlavor {
	return &rf.ResourceFlavor
}

// TopologyName sets the topology name
func (rf *ResourceFlavorWrapper) TopologyName(name string) *ResourceFlavorWrapper {
	rf.Spec.TopologyName = ptr.To(kueue.TopologyReference(name))
	return rf
}

// Label sets the label on the ResourceFlavor.
func (rf *ResourceFlavorWrapper) Label(k, v string) *ResourceFlavorWrapper {
	if rf.Labels == nil {
		rf.Labels = map[string]string{}
	}
	rf.Labels[k] = v
	return rf
}

// NodeLabel add a label kueue and value pair to the ResourceFlavor.
func (rf *ResourceFlavorWrapper) NodeLabel(k, v string) *ResourceFlavorWrapper {
	rf.Spec.NodeLabels[k] = v
	return rf
}

// Taint adds a taint to the ResourceFlavor.
func (rf *ResourceFlavorWrapper) Taint(t corev1.Taint) *ResourceFlavorWrapper {
	rf.Spec.NodeTaints = append(rf.Spec.NodeTaints, t)
	return rf
}

// Toleration  adds a taint to the ResourceFlavor.
func (rf *ResourceFlavorWrapper) Toleration(t corev1.Toleration) *ResourceFlavorWrapper {
	rf.Spec.Tolerations = append(rf.Spec.Tolerations, t)
	return rf
}

// Creation sets the creation timestamp of the LocalQueue.
func (rf *ResourceFlavorWrapper) Creation(t time.Time) *ResourceFlavorWrapper {
	rf.CreationTimestamp = metav1.NewTime(t)
	return rf
}

// TopologyWrapper wraps a Topology.
type TopologyWrapper struct{ kueue.Topology }

// MakeTopology creates a wrapper for a Topology.
func MakeTopology(name string) *TopologyWrapper {
	return &TopologyWrapper{kueue.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}}
}

// Levels sets the levels for a Topology.
func (t *TopologyWrapper) Levels(levels ...string) *TopologyWrapper {
	t.Spec.Levels = make([]kueue.TopologyLevel, len(levels))
	for i, level := range levels {
		t.Spec.Levels[i] = kueue.TopologyLevel{
			NodeLabel: level,
		}
	}
	return t
}

// Label adds a label to a Topology.
func (t *TopologyWrapper) Label(k, v string) *TopologyWrapper {
	if t.Labels == nil {
		t.Labels = make(map[string]string)
	}
	t.Labels[k] = v
	return t
}

func (t *TopologyWrapper) Obj() *kueue.Topology {
	return &t.Topology
}

type TopologyDomainAssignmentWrapper struct {
	tas.TopologyDomainAssignment
}

func MakeTopologyDomainAssignment(values []string, count int32) *TopologyDomainAssignmentWrapper {
	return &TopologyDomainAssignmentWrapper{
		TopologyDomainAssignment: tas.TopologyDomainAssignment{
			Values: values,
			Count:  count,
		},
	}
}

func (t *TopologyDomainAssignmentWrapper) Obj() tas.TopologyDomainAssignment {
	return t.TopologyDomainAssignment
}

type TopologyAssignmentWrapper struct {
	tas.TopologyAssignment
}

func MakeTopologyAssignment(levels []string) *TopologyAssignmentWrapper {
	return &TopologyAssignmentWrapper{
		TopologyAssignment: tas.TopologyAssignment{
			Levels: levels,
		},
	}
}

func (t *TopologyAssignmentWrapper) Levels(levels ...string) *TopologyAssignmentWrapper {
	t.TopologyAssignment.Levels = levels
	return t
}

func (t *TopologyAssignmentWrapper) Domains(domains ...tas.TopologyDomainAssignment) *TopologyAssignmentWrapper {
	t.TopologyAssignment.Domains = domains
	return t
}

func (t *TopologyAssignmentWrapper) Domain(domain tas.TopologyDomainAssignment) *TopologyAssignmentWrapper {
	t.TopologyAssignment.Domains = append(t.TopologyAssignment.Domains, domain)
	return t
}

func (t *TopologyAssignmentWrapper) Obj() *kueue.TopologyAssignment {
	return tas.V1Beta2From(&t.TopologyAssignment)
}

type PodSetAssignmentWrapper struct {
	kueue.PodSetAssignment
}

func MakePodSetAssignment(name kueue.PodSetReference) *PodSetAssignmentWrapper {
	return &PodSetAssignmentWrapper{
		PodSetAssignment: kueue.PodSetAssignment{
			Name:          name,
			Flavors:       make(map[corev1.ResourceName]kueue.ResourceFlavorReference),
			ResourceUsage: make(corev1.ResourceList),
			Count:         ptr.To[int32](1),
		},
	}
}

func (p *PodSetAssignmentWrapper) Obj() kueue.PodSetAssignment {
	return p.PodSetAssignment
}

func (p *PodSetAssignmentWrapper) Flavor(resource corev1.ResourceName, flavor kueue.ResourceFlavorReference) *PodSetAssignmentWrapper {
	if p.Flavors == nil {
		p.Flavors = make(map[corev1.ResourceName]kueue.ResourceFlavorReference)
	}
	p.Flavors[resource] = flavor
	return p
}

func (p *PodSetAssignmentWrapper) ResourceUsage(resourceName corev1.ResourceName, quantity string) *PodSetAssignmentWrapper {
	if p.PodSetAssignment.ResourceUsage == nil {
		p.PodSetAssignment.ResourceUsage = make(corev1.ResourceList)
	}
	p.PodSetAssignment.ResourceUsage[resourceName] = resource.MustParse(quantity)
	return p
}

func (p *PodSetAssignmentWrapper) Count(count int32) *PodSetAssignmentWrapper {
	p.PodSetAssignment.Count = ptr.To(count)
	return p
}

func (p *PodSetAssignmentWrapper) TopologyAssignment(ta *kueue.TopologyAssignment) *PodSetAssignmentWrapper {
	p.PodSetAssignment.TopologyAssignment = ta
	return p
}

func (p *PodSetAssignmentWrapper) DelayedTopologyRequest(state kueue.DelayedTopologyRequestState) *PodSetAssignmentWrapper {
	p.PodSetAssignment.DelayedTopologyRequest = ptr.To(state)
	return p
}

func (p *PodSetAssignmentWrapper) Assignment(r corev1.ResourceName, f kueue.ResourceFlavorReference, value string) *PodSetAssignmentWrapper {
	return p.Flavor(r, f).ResourceUsage(r, value)
}

type AdmissionCheckWrapper struct{ kueue.AdmissionCheck }

func MakeAdmissionCheck(name string) *AdmissionCheckWrapper {
	return &AdmissionCheckWrapper{
		AdmissionCheck: kueue.AdmissionCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

type AdmissionCheckStrategyRuleWrapper struct {
	kueue.AdmissionCheckStrategyRule
}

func MakeAdmissionCheckStrategyRule(name kueue.AdmissionCheckReference, flavors ...kueue.ResourceFlavorReference) *AdmissionCheckStrategyRuleWrapper {
	if len(flavors) == 0 {
		flavors = make([]kueue.ResourceFlavorReference, 0)
	}
	return &AdmissionCheckStrategyRuleWrapper{
		AdmissionCheckStrategyRule: kueue.AdmissionCheckStrategyRule{
			Name:      name,
			OnFlavors: flavors,
		},
	}
}

func (acs *AdmissionCheckStrategyRuleWrapper) OnFlavors(flavors []kueue.ResourceFlavorReference) *AdmissionCheckStrategyRuleWrapper {
	acs.AdmissionCheckStrategyRule.OnFlavors = flavors
	return acs
}

func (acs *AdmissionCheckStrategyRuleWrapper) Obj() *kueue.AdmissionCheckStrategyRule {
	return &acs.AdmissionCheckStrategyRule
}

func (ac *AdmissionCheckWrapper) Active(status metav1.ConditionStatus) *AdmissionCheckWrapper {
	apimeta.SetStatusCondition(&ac.Status.Conditions, metav1.Condition{
		Type:    kueue.AdmissionCheckActive,
		Status:  status,
		Reason:  "ByTest",
		Message: "by test",
	})
	return ac
}

func (ac *AdmissionCheckWrapper) Condition(cond metav1.Condition) *AdmissionCheckWrapper {
	apimeta.SetStatusCondition(&ac.Status.Conditions, cond)
	return ac
}

// Generation sets the generation of the AdmissionCheck.
func (ac *AdmissionCheckWrapper) Generation(num int64) *AdmissionCheckWrapper {
	ac.ObjectMeta.Generation = num
	return ac
}

func (ac *AdmissionCheckWrapper) ControllerName(c string) *AdmissionCheckWrapper {
	ac.Spec.ControllerName = c
	return ac
}

func (ac *AdmissionCheckWrapper) Parameters(apigroup, kind, name string) *AdmissionCheckWrapper {
	ac.Spec.Parameters = &kueue.AdmissionCheckParametersReference{
		APIGroup: apigroup,
		Kind:     kind,
		Name:     name,
	}
	return ac
}

func (ac *AdmissionCheckWrapper) Obj() *kueue.AdmissionCheck {
	return &ac.AdmissionCheck
}

// WorkloadPriorityClassWrapper wraps a WorkloadPriorityClass.
type WorkloadPriorityClassWrapper struct {
	kueue.WorkloadPriorityClass
}

// MakeWorkloadPriorityClass creates a wrapper for a WorkloadPriorityClass.
func MakeWorkloadPriorityClass(name string) *WorkloadPriorityClassWrapper {
	return &WorkloadPriorityClassWrapper{kueue.WorkloadPriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

// PriorityValue updates value of WorkloadPriorityClass.
func (p *WorkloadPriorityClassWrapper) PriorityValue(v int32) *WorkloadPriorityClassWrapper {
	p.Value = v
	return p
}

// Obj returns the inner WorkloadPriorityClass.
func (p *WorkloadPriorityClassWrapper) Obj() *kueue.WorkloadPriorityClass {
	return &p.WorkloadPriorityClass
}

type MultiKueueConfigWrapper struct {
	kueue.MultiKueueConfig
}

func MakeMultiKueueConfig(name string) *MultiKueueConfigWrapper {
	return &MultiKueueConfigWrapper{
		MultiKueueConfig: kueue.MultiKueueConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (mkc *MultiKueueConfigWrapper) Obj() *kueue.MultiKueueConfig {
	return &mkc.MultiKueueConfig
}

func (mkc *MultiKueueConfigWrapper) Clusters(clusters ...string) *MultiKueueConfigWrapper {
	mkc.Spec.Clusters = append(mkc.Spec.Clusters, clusters...)
	return mkc
}

type MultiKueueClusterWrapper struct {
	kueue.MultiKueueCluster
}

func MakeMultiKueueCluster(name string) *MultiKueueClusterWrapper {
	return &MultiKueueClusterWrapper{
		MultiKueueCluster: kueue.MultiKueueCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (mkc *MultiKueueClusterWrapper) Obj() *kueue.MultiKueueCluster {
	return &mkc.MultiKueueCluster
}

func (mkc *MultiKueueClusterWrapper) KubeConfig(locationType kueue.LocationType, location string) *MultiKueueClusterWrapper {
	mkc.Spec.ClusterSource.KubeConfig = &kueue.KubeConfig{
		Location:     location,
		LocationType: locationType,
	}
	return mkc
}

func (mkc *MultiKueueClusterWrapper) ClusterProfile(name string) *MultiKueueClusterWrapper {
	mkc.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{
		Name: name,
	}
	return mkc
}

func (mkc *MultiKueueClusterWrapper) Active(state metav1.ConditionStatus, reason, message string, generation int64) *MultiKueueClusterWrapper {
	cond := metav1.Condition{
		Type:               kueue.MultiKueueClusterActive,
		Status:             state,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
	apimeta.SetStatusCondition(&mkc.Status.Conditions, cond)
	return mkc
}

// Generation sets the generation of the MultiKueueCluster.
func (mkc *MultiKueueClusterWrapper) Generation(num int64) *MultiKueueClusterWrapper {
	mkc.ObjectMeta.Generation = num
	return mkc
}

// ProvisioningRequestConfigWrapper wraps a ProvisioningRequestConfig
type ProvisioningRequestConfigWrapper struct {
	kueue.ProvisioningRequestConfig
}

// MakeProvisioningRequestConfig creates a wrapper for a ProvisioningRequestConfig.
func MakeProvisioningRequestConfig(name string) *ProvisioningRequestConfigWrapper {
	return &ProvisioningRequestConfigWrapper{kueue.ProvisioningRequestConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

func (prc *ProvisioningRequestConfigWrapper) ProvisioningClass(pc string) *ProvisioningRequestConfigWrapper {
	prc.Spec.ProvisioningClassName = pc
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) Parameters(parameters map[string]kueue.Parameter) *ProvisioningRequestConfigWrapper {
	if prc.Spec.Parameters == nil {
		prc.Spec.Parameters = make(map[string]kueue.Parameter, len(parameters))
	}

	maps.Copy(prc.Spec.Parameters, parameters)

	return prc
}

func (prc *ProvisioningRequestConfigWrapper) WithParameter(key string, value kueue.Parameter) *ProvisioningRequestConfigWrapper {
	if prc.Spec.Parameters == nil {
		prc.Spec.Parameters = make(map[string]kueue.Parameter, 1)
	}

	prc.Spec.Parameters[key] = value
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) ManagedResources(r []corev1.ResourceName) *ProvisioningRequestConfigWrapper {
	prc.Spec.ManagedResources = r
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) WithManagedResource(managedResource corev1.ResourceName) *ProvisioningRequestConfigWrapper {
	prc.Spec.ManagedResources = append(prc.Spec.ManagedResources, managedResource)
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) RetryStrategy(retryStrategy *kueue.ProvisioningRequestRetryStrategy) *ProvisioningRequestConfigWrapper {
	prc.Spec.RetryStrategy = retryStrategy
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) PodSetUpdate(update kueue.ProvisioningRequestPodSetUpdates) *ProvisioningRequestConfigWrapper {
	prc.Spec.PodSetUpdates = &update
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) BaseBackoff(backoffBaseSeconds int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffBaseSeconds = &backoffBaseSeconds
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) MaxBackoff(backoffMaxSeconds int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffMaxSeconds = &backoffMaxSeconds
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) RetryLimit(backoffLimitCount int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffLimitCount = &backoffLimitCount
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) PodSetMergePolicy(mode kueue.ProvisioningRequestConfigPodSetMergePolicy) *ProvisioningRequestConfigWrapper {
	prc.Spec.PodSetMergePolicy = &mode
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) Clone() *ProvisioningRequestConfigWrapper {
	return &ProvisioningRequestConfigWrapper{ProvisioningRequestConfig: *prc.DeepCopy()}
}

func (prc *ProvisioningRequestConfigWrapper) Obj() *kueue.ProvisioningRequestConfig {
	return &prc.ProvisioningRequestConfig
}

type ClusterProfileWrapper struct {
	inventoryv1alpha1.ClusterProfile
}

func MakeClusterProfile(name, ns string) *ClusterProfileWrapper {
	return &ClusterProfileWrapper{
		ClusterProfile: inventoryv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: inventoryv1alpha1.ClusterProfileSpec{},
		},
	}
}

func (cpw *ClusterProfileWrapper) Obj() *inventoryv1alpha1.ClusterProfile {
	return &cpw.ClusterProfile
}

func (cpw *ClusterProfileWrapper) DisplayName(displayName string) *ClusterProfileWrapper {
	cpw.Spec.DisplayName = displayName
	return cpw
}

func (cpw *ClusterProfileWrapper) ClusterManager(clusterManagerName string) *ClusterProfileWrapper {
	cpw.Spec.ClusterManager = inventoryv1alpha1.ClusterManager{
		Name: clusterManagerName,
	}
	return cpw
}
