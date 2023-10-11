package cache

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errQueueAlreadyExists = errors.New("queue already exists")
)

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds admitted workloads.
type ClusterQueue struct {
	Name              string
	Cohort            *Cohort
	ResourceGroups    []ResourceGroup
	RGByResource      map[corev1.ResourceName]*ResourceGroup
	Usage             FlavorResourceQuantities
	Workloads         map[string]*workload.Info
	WorkloadsNotReady sets.Set[string]
	NamespaceSelector labels.Selector
	Preemption        kueue.ClusterQueuePreemption
	Status            metrics.ClusterQueueStatus
	AdmissionChecks   sets.Set[string]

	// The following fields are not populated in a snapshot.

	// Key is localQueue's key (namespace/name).
	localQueues               map[string]*queue
	podsReadyTracking         bool
	hasMissingFlavors         bool
	hasMissingAdmissionChecks bool
}

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	Members sets.Set[*ClusterQueue]

	// These fields are only populated for a snapshot.
	RequestableResources FlavorResourceQuantities
	Usage                FlavorResourceQuantities
}

type ResourceGroup struct {
	CoveredResources sets.Set[corev1.ResourceName]
	Flavors          []FlavorQuotas
	// The set of key labels from all flavors.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys sets.Set[string]
}

// FlavorQuotas holds a processed ClusterQueue flavor quota.
type FlavorQuotas struct {
	Name      kueue.ResourceFlavorReference
	Resources map[corev1.ResourceName]*ResourceQuota
}

type ResourceQuota struct {
	Nominal        int64
	BorrowingLimit *int64
}

type FlavorResourceQuantities map[kueue.ResourceFlavorReference]map[corev1.ResourceName]int64

type queue struct {
	key               string
	admittedWorkloads int
	usage             FlavorResourceQuantities
}

func newCohort(name string, size int) *Cohort {
	return &Cohort{
		Name:    name,
		Members: make(sets.Set[*ClusterQueue], size),
	}
}

func (c *Cohort) CanFit(q FlavorResourceQuantities) bool {
	for flavor, qResources := range q {
		if cohortResources, flavorFound := c.RequestableResources[flavor]; flavorFound {
			cohortUsage := c.Usage[flavor]
			for resource, value := range qResources {
				available := cohortResources[resource] - cohortUsage[resource]
				if available < value {
					return false
				}
			}
		} else {
			return false
		}
	}
	return true
}

func (c *ClusterQueue) IsBorrowing() bool {
	if c.Cohort == nil || len(c.Usage) == 0 {
		return false
	}
	for _, rg := range c.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			if flvUsage, isUsing := c.Usage[flvQuotas.Name]; isUsing {
				for rName, rQuota := range flvQuotas.Resources {
					used := flvUsage[rName]
					if used > rQuota.Nominal {
						return true
					}
				}
			}
		}
	}
	return false
}

func (c *ClusterQueue) Active() bool {
	return c.Status == active
}

var defaultPreemption = kueue.ClusterQueuePreemption{
	ReclaimWithinCohort: kueue.PreemptionPolicyNever,
	WithinClusterQueue:  kueue.PreemptionPolicyNever,
}

func (c *ClusterQueue) update(in *kueue.ClusterQueue, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, admissionChecks sets.Set[string]) error {
	c.updateResourceGroups(in.Spec.ResourceGroups)
	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	// Cleanup removed flavors or resources.
	usedFlavorResources := make(FlavorResourceQuantities)
	for _, rg := range in.Spec.ResourceGroups {
		for _, f := range rg.Flavors {
			existingUsedResources := c.Usage[f.Name]
			usedResources := make(map[corev1.ResourceName]int64, len(f.Resources))
			for _, r := range f.Resources {
				usedResources[r.Name] = existingUsedResources[r.Name]
			}
			usedFlavorResources[f.Name] = usedResources
		}
	}

	c.AdmissionChecks = sets.New(in.Spec.AdmissionChecks...)

	c.Usage = usedFlavorResources
	c.UpdateWithFlavors(resourceFlavors)
	c.updateWithAdmissionChecks(admissionChecks)

	if in.Spec.Preemption != nil {
		c.Preemption = *in.Spec.Preemption
	} else {
		c.Preemption = defaultPreemption
	}

	return nil
}

func (c *ClusterQueue) updateResourceGroups(in []kueue.ResourceGroup) {
	c.ResourceGroups = make([]ResourceGroup, len(in))
	for i, rgIn := range in {
		rg := &c.ResourceGroups[i]
		*rg = ResourceGroup{
			CoveredResources: sets.New(rgIn.CoveredResources...),
			Flavors:          make([]FlavorQuotas, 0, len(rgIn.Flavors)),
		}
		for i := range rgIn.Flavors {
			fIn := &rgIn.Flavors[i]
			fQuotas := FlavorQuotas{
				Name:      fIn.Name,
				Resources: make(map[corev1.ResourceName]*ResourceQuota, len(fIn.Resources)),
			}
			for _, rIn := range fIn.Resources {
				rQuota := ResourceQuota{
					Nominal: workload.ResourceValue(rIn.Name, rIn.NominalQuota),
				}
				if rIn.BorrowingLimit != nil {
					rQuota.BorrowingLimit = ptr.To(workload.ResourceValue(rIn.Name, *rIn.BorrowingLimit))
				}
				fQuotas.Resources[rIn.Name] = &rQuota
			}
			rg.Flavors = append(rg.Flavors, fQuotas)
		}
	}
	c.UpdateRGByResource()
}

func (c *ClusterQueue) UpdateRGByResource() {
	c.RGByResource = make(map[corev1.ResourceName]*ResourceGroup)
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		for rName := range rg.CoveredResources {
			c.RGByResource[rName] = rg
		}
	}
}

func (c *ClusterQueue) updateQueueStatus() {
	status := active
	if c.hasMissingFlavors || c.hasMissingAdmissionChecks {
		status = pending
	}
	if c.Status == terminating {
		status = terminating
	}
	if status != c.Status {
		c.Status = status
		metrics.ReportClusterQueueStatus(c.Name, c.Status)
	}
}

func (c *ClusterQueue) inactiveReason() (string, string) {
	switch c.Status {
	case terminating:
		return "Terminating", "Can't admit new workloads; clusterQueue is terminating"
	case pending:
		switch {
		case c.hasMissingFlavors && c.hasMissingAdmissionChecks:
			return "FlavorAndCheckNotFound", "Can't admit new workloads; some resourceFlavors and admissionChecks are not found"
		case c.hasMissingFlavors:
			return "FlavorNotFound", "Can't admit new workloads; some resourceFlavors are not found"
		default:
			return "CheckNotFound", "Can't admit new workloads; some admissionChecks are not found"

		}
	}
	return "Ready", "Can admit new flavors"
}

// UpdateWithFlavors updates a ClusterQueue based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *ClusterQueue) UpdateWithFlavors(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.hasMissingFlavors = c.updateLabelKeys(flavors)
	c.updateQueueStatus()
}

func (c *ClusterQueue) updateLabelKeys(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) bool {
	var flavorNotFound bool
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		if len(rg.Flavors) == 0 {
			rg.LabelKeys = nil
			continue
		}
		keys := sets.New[string]()
		for _, rf := range rg.Flavors {
			if flv, exist := flavors[rf.Name]; exist {
				for k := range flv.Spec.NodeLabels {
					keys.Insert(k)
				}
			} else {
				flavorNotFound = true
			}
		}

		if keys.Len() > 0 {
			rg.LabelKeys = keys
		}
	}

	return flavorNotFound
}

// updateWithAdmissionChecks updates a ClusterQueue based on the passed AdmissionChecks set.
func (c *ClusterQueue) updateWithAdmissionChecks(checks sets.Set[string]) {
	hasMissing := false
	for ac := range c.AdmissionChecks {
		if !checks.Has(ac) {
			hasMissing = true
			break
		}
	}

	if hasMissing != c.hasMissingAdmissionChecks {
		c.hasMissingAdmissionChecks = hasMissing
		c.updateQueueStatus()
	}
}

func (c *ClusterQueue) addWorkload(w *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) error {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		return fmt.Errorf("workload already exists in ClusterQueue")
	}
	wi := workload.NewInfo(w, pts)
	c.Workloads[k] = wi
	c.updateWorkloadUsage(wi, 1)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Insert(k)
	}
	reportAdmittedActiveWorkloads(wi.ClusterQueue, len(c.Workloads))
	return nil
}

func (c *ClusterQueue) deleteWorkload(w *kueue.Workload) {
	k := workload.Key(w)
	wi, exist := c.Workloads[k]
	if !exist {
		return
	}
	c.updateWorkloadUsage(wi, -1)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Delete(k)
	}
	delete(c.Workloads, k)
	reportAdmittedActiveWorkloads(wi.ClusterQueue, len(c.Workloads))
}

// updateWorkloadUsage updates the usage of the ClusterQueue for the workload
// and the number of admitted workloads for local queues.
func (c *ClusterQueue) updateWorkloadUsage(wi *workload.Info, m int64) {
	updateUsage(wi, c.Usage, m)
	qKey := workload.QueueKey(wi.Obj)
	if _, ok := c.localQueues[qKey]; ok {
		updateUsage(wi, c.localQueues[qKey].usage, m)
		c.localQueues[qKey].admittedWorkloads += int(m)
	}
}

func updateUsage(wi *workload.Info, flvUsage FlavorResourceQuantities, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResFlv := range ps.Flavors {
			v, wlResExist := ps.Requests[wlRes]
			flv, flvExist := flvUsage[wlResFlv]
			if flvExist && wlResExist {
				if _, exists := flv[wlRes]; exists {
					flv[wlRes] += v * m
				}
			}
		}
	}
}

func (c *ClusterQueue) addLocalQueue(q *kueue.LocalQueue) error {
	qKey := queueKey(q)
	if _, ok := c.localQueues[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	qImpl := &queue{
		key:               qKey,
		admittedWorkloads: 0,
		usage:             make(FlavorResourceQuantities),
	}
	if err := qImpl.resetFlavorsAndResources(c.Usage); err != nil {
		return err
	}
	for _, wl := range c.Workloads {
		if workloadBelongsToLocalQueue(wl.Obj, q) {
			updateUsage(wl, qImpl.usage, 1)
			qImpl.admittedWorkloads++
		}
	}
	c.localQueues[qKey] = qImpl
	return nil
}

func (c *ClusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	delete(c.localQueues, qKey)
}

func (c *ClusterQueue) flavorInUse(flavor string) bool {
	for _, rg := range c.ResourceGroups {
		for _, f := range rg.Flavors {
			if kueue.ResourceFlavorReference(flavor) == f.Name {
				return true
			}
		}
	}
	return false
}

func (q *queue) resetFlavorsAndResources(cqUsage FlavorResourceQuantities) error {
	// Clean up removed flavors or resources.
	usedFlavorResources := make(FlavorResourceQuantities)
	for cqFlv, cqRes := range cqUsage {
		existingUsedResources := q.usage[cqFlv]
		usedResources := make(map[corev1.ResourceName]int64, len(cqRes))
		for rName := range cqRes {
			usedResources[rName] = existingUsedResources[rName]
		}
		usedFlavorResources[cqFlv] = usedResources
	}
	q.usage = usedFlavorResources
	return nil
}

func workloadBelongsToLocalQueue(wl *kueue.Workload, q *kueue.LocalQueue) bool {
	return wl.Namespace == q.Namespace && wl.Spec.QueueName == q.Name
}

func reportAdmittedActiveWorkloads(cqName string, val int) {
	metrics.AdmittedActiveWorkloads.WithLabelValues(cqName).Set(float64(val))
}
