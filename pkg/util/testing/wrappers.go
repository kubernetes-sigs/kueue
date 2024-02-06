/*
Copyright 2022 The Kubernetes Authors.

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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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

// MakeWorkload creates a wrapper for a Workload with a single
// pod with a single container.
func MakeWorkload(name, ns string) *WorkloadWrapper {
	return &WorkloadWrapper{kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{
				*MakePodSet("main", 1).Obj(),
			},
		},
	}}
}

func (w *WorkloadWrapper) Obj() *kueue.Workload {
	return &w.Workload
}

func (w *WorkloadWrapper) Clone() *WorkloadWrapper {
	return &WorkloadWrapper{Workload: *w.DeepCopy()}
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

func (w *WorkloadWrapper) Queue(q string) *WorkloadWrapper {
	w.Spec.QueueName = q
	return w
}

func (w *WorkloadWrapper) Active(a bool) *WorkloadWrapper {
	w.Spec.Active = ptr.To(a)
	return w
}

// ReserveQuota sets workload admission and adds a "QuotaReserved" status condition
func (w *WorkloadWrapper) ReserveQuota(a *kueue.Admission) *WorkloadWrapper {
	w.Status.Admission = a
	w.Status.Conditions = []metav1.Condition{{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "AdmittedByTest",
		Message:            fmt.Sprintf("Admitted by ClusterQueue %s", w.Status.Admission.ClusterQueue),
	}}
	return w
}

func (w *WorkloadWrapper) Admitted(a bool) *WorkloadWrapper {
	cond := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "ByTest",
		Message:            fmt.Sprintf("Admitted by ClusterQueue %s", w.Status.Admission.ClusterQueue),
	}
	if !a {
		cond.Status = metav1.ConditionFalse
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

func (w *WorkloadWrapper) AdmissionChecks(checks ...kueue.AdmissionCheckState) *WorkloadWrapper {
	w.Status.AdmissionChecks = checks
	return w
}

func (w *WorkloadWrapper) OwnerReference(gvk schema.GroupVersionKind, name, uid string, controller, blockDeletion *bool) *WorkloadWrapper {
	w.OwnerReferences = append(w.OwnerReferences, metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               name,
		UID:                types.UID(uid),
		Controller:         controller,
		BlockOwnerDeletion: blockDeletion,
	})
	return w
}

func (w *WorkloadWrapper) Annotations(kv map[string]string) *WorkloadWrapper {
	w.ObjectMeta.Annotations = kv
	return w
}

// DeletionTimestamp sets a deletion timestamp for the workload.
func (w *WorkloadWrapper) DeletionTimestamp(t time.Time) *WorkloadWrapper {
	w.Workload.DeletionTimestamp = ptr.To(metav1.NewTime(t).Rfc3339Copy())
	return w
}

type PodSetWrapper struct{ kueue.PodSet }

func MakePodSet(name string, count int) *PodSetWrapper {
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

func (p *PodSetWrapper) PriorityClass(pc string) *PodSetWrapper {
	p.Template.Spec.PriorityClassName = pc
	return p
}

func (p *PodSetWrapper) RuntimeClass(name string) *PodSetWrapper {
	p.Template.Spec.RuntimeClassName = &name
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

func MakeAdmission(cq string, podSetNames ...string) *AdmissionWrapper {
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
	w.PodSetAssignments[0].Flavors[r] = f
	w.PodSetAssignments[0].ResourceUsage[r] = resource.MustParse(value)
	return w
}

func (w *AdmissionWrapper) AssignmentPodCount(value int32) *AdmissionWrapper {
	w.PodSetAssignments[0].Count = ptr.To(value)
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

// Obj returns the inner LocalQueue.
func (q *LocalQueueWrapper) Obj() *kueue.LocalQueue {
	return &q.LocalQueue
}

// ClusterQueue updates the clusterQueue the queue points to.
func (q *LocalQueueWrapper) ClusterQueue(c string) *LocalQueueWrapper {
	q.Spec.ClusterQueue = kueue.ClusterQueueReference(c)
	return q
}

// PendingWorkloads updates the pendingWorkloads in status.
func (q *LocalQueueWrapper) PendingWorkloads(n int32) *LocalQueueWrapper {
	q.Status.PendingWorkloads = n
	return q
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
func (c *ClusterQueueWrapper) Cohort(cohort string) *ClusterQueueWrapper {
	c.Spec.Cohort = cohort
	return c
}

// ResourceGroup adds a ResourceGroup with flavors.
func (c *ClusterQueueWrapper) ResourceGroup(flavors ...kueue.FlavorQuotas) *ClusterQueueWrapper {
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
	c.Spec.ResourceGroups = append(c.Spec.ResourceGroups, rg)
	return c
}

// AdmissionChecks replaces the queue additional checks
func (c *ClusterQueueWrapper) AdmissionChecks(checks ...string) *ClusterQueueWrapper {
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

// Preemption sets the preeemption policies.
func (c *ClusterQueueWrapper) Preemption(p kueue.ClusterQueuePreemption) *ClusterQueueWrapper {
	c.Spec.Preemption = &p
	return c
}

// Preemption sets the preeemption policies.
func (c *ClusterQueueWrapper) FlavorFungibility(p kueue.FlavorFungibility) *ClusterQueueWrapper {
	c.Spec.FlavorFungibility = &p
	return c
}

func (c *ClusterQueueWrapper) StopPolicy(p kueue.StopPolicy) *ClusterQueueWrapper {
	c.Spec.StopPolicy = &p
	return c
}

func (c *ClusterQueueWrapper) Condition(conditionType string, status metav1.ConditionStatus, reason, message string) *ClusterQueueWrapper {
	apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
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

func (f *FlavorQuotasWrapper) Resource(name corev1.ResourceName, qs ...string) *FlavorQuotasWrapper {
	rq := kueue.ResourceQuota{
		Name: name,
	}
	if len(qs) > 0 {
		rq.NominalQuota = resource.MustParse(qs[0])
	}
	if len(qs) > 1 {
		rq.BorrowingLimit = ptr.To(resource.MustParse(qs[1]))
	}
	if len(qs) > 2 {
		panic("Must have at most 2 quantities for nominalquota and borrowingLimit")
	}
	f.Resources = append(f.Resources, rq)
	return f
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

// Label add a label kueue and value pair to the ResourceFlavor.
func (rf *ResourceFlavorWrapper) Label(k, v string) *ResourceFlavorWrapper {
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
	//nothing
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
	kueuealpha.MultiKueueConfig
}

func MakeMultiKueueConfig(name string) *MultiKueueConfigWrapper {
	return &MultiKueueConfigWrapper{
		MultiKueueConfig: kueuealpha.MultiKueueConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (mkc *MultiKueueConfigWrapper) Obj() *kueuealpha.MultiKueueConfig {
	return &mkc.MultiKueueConfig
}

func (mkc *MultiKueueConfigWrapper) Clusters(clusters ...string) *MultiKueueConfigWrapper {
	mkc.Spec.Clusters = append(mkc.Spec.Clusters, clusters...)
	return mkc
}

type MultiKueueClusterWrapper struct {
	kueuealpha.MultiKueueCluster
}

func MakeMultiKueueCluster(name string) *MultiKueueClusterWrapper {
	return &MultiKueueClusterWrapper{
		MultiKueueCluster: kueuealpha.MultiKueueCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (mkc *MultiKueueClusterWrapper) Obj() *kueuealpha.MultiKueueCluster {
	return &mkc.MultiKueueCluster
}

func (mkc *MultiKueueClusterWrapper) KubeConfig(LocationType kueuealpha.LocationType, location string) *MultiKueueClusterWrapper {
	mkc.Spec.KubeConfig = kueuealpha.KubeConfig{
		Location:     location,
		LocationType: LocationType,
	}
	return mkc
}

func (mkc *MultiKueueClusterWrapper) Active(state metav1.ConditionStatus, reason, message string) *MultiKueueClusterWrapper {
	cond := metav1.Condition{
		Type:    kueuealpha.MultiKueueClusterActive,
		Status:  state,
		Reason:  reason,
		Message: message,
	}
	apimeta.SetStatusCondition(&mkc.Status.Conditions, cond)
	return mkc
}
