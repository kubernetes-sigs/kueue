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

package testing

import (
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilResource "sigs.k8s.io/kueue/pkg/util/resource"
)

// PriorityClassWrapper wraps a PriorityClass.
type PriorityClassWrapper struct {
	schedulingv1.PriorityClass
}

// MakePriorityClass creates a wrapper for a PriorityClass.
func MakePriorityClass(name string) *PriorityClassWrapper {
	return &PriorityClassWrapper{schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

// PriorityValue update value of PriorityClass。
func (p *PriorityClassWrapper) PriorityValue(v int32) *PriorityClassWrapper {
	p.Value = v
	return p
}

// Obj returns the inner PriorityClass.
func (p *PriorityClassWrapper) Obj() *schedulingv1.PriorityClass {
	return &p.PriorityClass
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

// ReserveQuota sets workload admission and adds a "QuotaReserved" status condition
func (w *WorkloadWrapper) ReserveQuota(a *kueue.Admission) *WorkloadWrapper {
	return w.ReserveQuotaAt(a, time.Now())
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

func (w *WorkloadWrapper) Admitted(a bool) *WorkloadWrapper {
	return w.AdmittedAt(a, time.Now())
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
	cond := metav1.Condition{
		Type:               kueue.WorkloadFinished,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ByTest",
		Message:            "Finished by test",
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, cond)
	return w
}

func (w *WorkloadWrapper) Evicted() *WorkloadWrapper {
	cond := metav1.Condition{
		Type:               kueue.WorkloadEvicted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
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

func (w *WorkloadWrapper) PriorityClass(priorityClassName string) *WorkloadWrapper {
	w.Spec.PriorityClassName = priorityClassName
	return w
}

func (w *WorkloadWrapper) RuntimeClass(name string) *WorkloadWrapper {
	for i := range w.Spec.PodSets {
		w.Spec.PodSets[i].Template.Spec.RuntimeClassName = &name
	}
	return w
}

func (w *WorkloadWrapper) Priority(priority int32) *WorkloadWrapper {
	w.Spec.Priority = &priority
	return w
}

func (w *WorkloadWrapper) PriorityClassSource(source string) *WorkloadWrapper {
	w.Spec.PriorityClassSource = source
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

func (w *WorkloadWrapper) SetOrReplaceCondition(condition metav1.Condition) *WorkloadWrapper {
	existingCondition := apimeta.FindStatusCondition(w.Status.Conditions, condition.Type)
	if existingCondition != nil {
		apimeta.RemoveStatusCondition(&w.Status.Conditions, condition.Type)
	}
	apimeta.SetStatusCondition(&w.Status.Conditions, condition)
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
	w.Status.AccumulatedPastExexcutionTimeSeconds = &v
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

func (w *WorkloadWrapper) NominatedClusterNames(nominatedClusterNames []string) *WorkloadWrapper {
	w.Status.NominatedClusterNames = nominatedClusterNames
	return w
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

func (p *PodSetWrapper) PreferredTopologyRequest(level string) *PodSetWrapper {
	if p.TopologyRequest == nil {
		p.TopologyRequest = &kueue.PodSetTopologyRequest{}
	}
	p.TopologyRequest.Preferred = &level
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

// AdmissionWrapper wraps an Admission
type AdmissionWrapper struct{ kueue.Admission }

func MakeAdmission(cq string, podSetNames ...kueue.PodSetReference) *AdmissionWrapper {
	wrap := &AdmissionWrapper{kueue.Admission{
		ClusterQueue: kueue.ClusterQueueReference(cq),
	}}

	if len(podSetNames) == 0 {
		wrap.PodSetAssignments = []kueue.PodSetAssignment{
			{
				Name:          kueue.DefaultPodSetName,
				Flavors:       make(map[corev1.ResourceName]kueue.ResourceFlavorReference),
				ResourceUsage: make(corev1.ResourceList),
				Count:         ptr.To[int32](1),
			},
		}
		return wrap
	}

	var psFlavors []kueue.PodSetAssignment
	for _, name := range podSetNames {
		psFlavors = append(psFlavors, kueue.PodSetAssignment{
			Name:          name,
			Flavors:       make(map[corev1.ResourceName]kueue.ResourceFlavorReference),
			ResourceUsage: make(corev1.ResourceList),
			Count:         ptr.To[int32](1),
		})
	}
	wrap.PodSetAssignments = psFlavors
	return wrap
}

func (w *AdmissionWrapper) Obj() *kueue.Admission {
	return &w.Admission
}

func (w *AdmissionWrapper) Assignment(r corev1.ResourceName, f kueue.ResourceFlavorReference, value string) *AdmissionWrapper {
	w.AssignmentWithIndex(0, r, f, value)
	return w
}

func (w *AdmissionWrapper) AssignmentPodCount(value int32) *AdmissionWrapper {
	w.AssignmentPodCountWithIndex(0, value)
	return w
}

func (w *AdmissionWrapper) TopologyAssignment(ts *kueue.TopologyAssignment) *AdmissionWrapper {
	w.TopologyAssignmentWithIndex(0, ts)
	return w
}

func (w *AdmissionWrapper) DelayedTopologyRequest(state kueue.DelayedTopologyRequestState) *AdmissionWrapper {
	w.DelayedTopologyRequestWithIndex(0, state)
	return w
}

func (w *AdmissionWrapper) AssignmentWithIndex(index int32, r corev1.ResourceName, f kueue.ResourceFlavorReference, value string) *AdmissionWrapper {
	w.PodSetAssignments[index].Flavors[r] = f
	w.PodSetAssignments[index].ResourceUsage[r] = resource.MustParse(value)
	return w
}

func (w *AdmissionWrapper) AssignmentPodCountWithIndex(index, value int32) *AdmissionWrapper {
	w.PodSetAssignments[index].Count = ptr.To(value)
	return w
}

func (w *AdmissionWrapper) TopologyAssignmentWithIndex(index int32, ts *kueue.TopologyAssignment) *AdmissionWrapper {
	w.PodSetAssignments[index].TopologyAssignment = ts
	return w
}

func (w *AdmissionWrapper) DelayedTopologyRequestWithIndex(index int32, state kueue.DelayedTopologyRequestState) *AdmissionWrapper {
	w.PodSetAssignments[index].DelayedTopologyRequest = ptr.To(state)
	return w
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
func (q *LocalQueueWrapper) FairSharingStatus(status *kueue.FairSharingStatus) *LocalQueueWrapper {
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
				WhenCanBorrow:  kueue.Borrow,
				WhenCanPreempt: kueue.TryNextFlavor,
			},
		},
	}}
}

// Obj returns the inner ClusterQueue.
func (c *ClusterQueueWrapper) Obj() *kueue.ClusterQueue {
	return &c.ClusterQueue
}

// Cohort sets the borrowing cohort.
func (c *ClusterQueueWrapper) Cohort(cohort kueue.CohortReference) *ClusterQueueWrapper {
	c.Spec.Cohort = cohort
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

// AdmissionChecks replaces the queue additional checks
func (c *ClusterQueueWrapper) AdmissionChecks(checks ...kueue.AdmissionCheckReference) *ClusterQueueWrapper {
	c.Spec.AdmissionChecks = checks
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
type TopologyWrapper struct{ kueuealpha.Topology }

// MakeTopology creates a wrapper for a Topology.
func MakeTopology(name string) *TopologyWrapper {
	return &TopologyWrapper{kueuealpha.Topology{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}}
}

// Levels sets the levels for a Topology.
func (t *TopologyWrapper) Levels(levels ...string) *TopologyWrapper {
	t.Spec.Levels = make([]kueuealpha.TopologyLevel, len(levels))
	for i, level := range levels {
		t.Spec.Levels[i] = kueuealpha.TopologyLevel{
			NodeLabel: level,
		}
	}
	return t
}

func (t *TopologyWrapper) Obj() *kueuealpha.Topology {
	return &t.Topology
}

// RuntimeClassWrapper wraps a RuntimeClass.
type RuntimeClassWrapper struct{ nodev1.RuntimeClass }

// MakeRuntimeClass creates a wrapper for a Runtime.
func MakeRuntimeClass(name, handler string) *RuntimeClassWrapper {
	return &RuntimeClassWrapper{nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Handler: handler,
	}}
}

// PodOverhead adds an Overhead to the RuntimeClass.
func (rc *RuntimeClassWrapper) PodOverhead(resources corev1.ResourceList) *RuntimeClassWrapper {
	rc.Overhead = &nodev1.Overhead{
		PodFixed: resources,
	}
	return rc
}

// Obj returns the inner flavor.
func (rc *RuntimeClassWrapper) Obj() *nodev1.RuntimeClass {
	return &rc.RuntimeClass
}

type LimitRangeWrapper struct{ corev1.LimitRange }

func MakeLimitRange(name, namespace string) *LimitRangeWrapper {
	return &LimitRangeWrapper{
		LimitRange: corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{
					{
						Type:                 corev1.LimitTypeContainer,
						Max:                  corev1.ResourceList{},
						Min:                  corev1.ResourceList{},
						Default:              corev1.ResourceList{},
						DefaultRequest:       corev1.ResourceList{},
						MaxLimitRequestRatio: corev1.ResourceList{},
					},
				},
			},
		},
	}
}

func (lr *LimitRangeWrapper) WithType(t corev1.LimitType) *LimitRangeWrapper {
	lr.Spec.Limits[0].Type = t
	return lr
}

func (lr *LimitRangeWrapper) WithValue(member string, t corev1.ResourceName, q string) *LimitRangeWrapper {
	target := lr.Spec.Limits[0].Max
	switch member {
	case "Min":
		target = lr.Spec.Limits[0].Min
	case "DefaultRequest":
		target = lr.Spec.Limits[0].DefaultRequest
	case "Default":
		target = lr.Spec.Limits[0].Default
	case "Max":
	case "MaxLimitRequestRatio":
		target = lr.Spec.Limits[0].MaxLimitRequestRatio
	// nothing
	default:
		panic("Unexpected member " + member)
	}
	target[t] = resource.MustParse(q)
	return lr
}

func (lr *LimitRangeWrapper) Obj() *corev1.LimitRange {
	return &lr.LimitRange
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
	mkc.Spec.KubeConfig = kueue.KubeConfig{
		Location:     location,
		LocationType: locationType,
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

// ContainerWrapper wraps a corev1.Container.
type ContainerWrapper struct{ corev1.Container }

// MakeContainer wraps a ContainerWrapper with an empty ResourceList.
func MakeContainer() *ContainerWrapper {
	return &ContainerWrapper{
		corev1.Container{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{},
			},
		},
	}
}

// Obj returns the inner corev1.Container.
func (c *ContainerWrapper) Obj() *corev1.Container {
	return &c.Container
}

// Name sets the name of the container.
func (c *ContainerWrapper) Name(name string) *ContainerWrapper {
	c.Container.Name = name
	return c
}

// WithResourceReq appends a resource request to the container.
func (c *ContainerWrapper) WithResourceReq(resourceName corev1.ResourceName, quantity string) *ContainerWrapper {
	requests := utilResource.MergeResourceListKeepFirst(c.Resources.Requests, corev1.ResourceList{
		resourceName: resource.MustParse(quantity),
	})
	c.Resources.Requests = requests

	return c
}

// WithResourceLimit appends a resource limit to the container.
func (c *ContainerWrapper) WithResourceLimit(resourceName corev1.ResourceName, quantity string) *ContainerWrapper {
	limits := utilResource.MergeResourceListKeepFirst(c.Resources.Limits, corev1.ResourceList{
		resourceName: resource.MustParse(quantity),
	})
	c.Resources.Limits = limits

	return c
}

// AsSidecar makes the container a sidecar when used as an Init Container.
func (c *ContainerWrapper) AsSidecar() *ContainerWrapper {
	c.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)

	return c
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

type PodTemplateWrapper struct {
	corev1.PodTemplate
}

func MakePodTemplate(name, namespace string) *PodTemplateWrapper {
	return &PodTemplateWrapper{
		corev1.PodTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
	}
}

func (p *PodTemplateWrapper) Obj() *corev1.PodTemplate {
	return &p.PodTemplate
}

func (p *PodTemplateWrapper) Clone() *PodTemplateWrapper {
	return &PodTemplateWrapper{PodTemplate: *p.DeepCopy()}
}

func (p *PodTemplateWrapper) Label(k, v string) *PodTemplateWrapper {
	if p.Labels == nil {
		p.Labels = make(map[string]string)
	}
	p.Labels[k] = v
	return p
}

func (p *PodTemplateWrapper) Containers(containers ...corev1.Container) *PodTemplateWrapper {
	p.Template.Spec.Containers = containers
	return p
}

func (p *PodTemplateWrapper) NodeSelector(k, v string) *PodTemplateWrapper {
	if p.Template.Spec.NodeSelector == nil {
		p.Template.Spec.NodeSelector = make(map[string]string)
	}
	p.Template.Spec.NodeSelector[k] = v
	return p
}

func (p *PodTemplateWrapper) Toleration(toleration corev1.Toleration) *PodTemplateWrapper {
	p.Template.Spec.Tolerations = append(p.Template.Spec.Tolerations, toleration)
	return p
}

func (p *PodTemplateWrapper) PriorityClass(pc string) *PodTemplateWrapper {
	p.Template.Spec.PriorityClassName = pc
	return p
}

func (p *PodTemplateWrapper) RequiredDuringSchedulingIgnoredDuringExecution(nodeSelectorTerms []corev1.NodeSelectorTerm) *PodTemplateWrapper {
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

func (p *PodTemplateWrapper) ControllerReference(gvk schema.GroupVersionKind, name, uid string) *PodTemplateWrapper {
	AppendOwnerReference(&p.PodTemplate, gvk, name, uid, ptr.To(true), ptr.To(true))
	return p
}

type NamespaceWrapper struct {
	corev1.Namespace
}

func MakeNamespaceWrapper(name string) *NamespaceWrapper {
	return &NamespaceWrapper{
		corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (w *NamespaceWrapper) Clone() *NamespaceWrapper {
	return &NamespaceWrapper{Namespace: *w.DeepCopy()}
}

func (w *NamespaceWrapper) Obj() *corev1.Namespace {
	return &w.Namespace
}

func (w *NamespaceWrapper) GenerateName(generateName string) *NamespaceWrapper {
	w.Namespace.GenerateName = generateName
	return w
}

func (w *NamespaceWrapper) Label(k, v string) *NamespaceWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[k] = v
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
