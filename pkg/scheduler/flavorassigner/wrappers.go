package flavorassigner

import (
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
)

// AssignmentWrapper wraps an Assignment.
type AssignmentWrapper struct{ Assignment }

// MakeAssignment creates a wrapper for an Assignment.
func MakeAssignment() *AssignmentWrapper {
	return &AssignmentWrapper{Assignment{
		Usage: make(resources.FlavorResourceQuantities),
	}}
}

// WithPodSet adds a PodSet to the Assignment.
func (a *AssignmentWrapper) WithPodSet(podSet PodSetAssignment) *AssignmentWrapper {
	if a.PodSets == nil {
		a.PodSets = make([]PodSetAssignment, 0)
	}
	a.PodSets = append(a.PodSets, podSet)
	return a
}

// WithBorrowing makes the Assignment a borrowing assignment.
func (a *AssignmentWrapper) WithBorrowing() *AssignmentWrapper {
	a.Borrowing = true
	return a
}

// WithFlavorResource adds a resource usage to the Assignment.
func (a *AssignmentWrapper) WithFlavorResource(fr resources.FlavorResource, quantity int64) *AssignmentWrapper {
	if a.Usage == nil {
		a.Usage = make(resources.FlavorResourceQuantities)
	}
	a.Usage[fr] = quantity
	return a
}

// Obj returns the inner Assignment.
func (a *AssignmentWrapper) Obj() *Assignment {
	return &a.Assignment
}

// PodSetAssignmentWrapper wraps a PodSetAssignment.
type PodSetAssignmentWrapper struct {
	PodSetAssignment
}

// MakePodSetAssignment creates a wrapper for a PodSetAssignment.
func MakePodSetAssignment(name string, count int32) *PodSetAssignmentWrapper {
	psa := &PodSetAssignmentWrapper{PodSetAssignment{}}
	return psa.WithName(name).WithCount(count)
}

// WithName sets the Name of the PodSetAssignment.
func (p *PodSetAssignmentWrapper) WithName(name string) *PodSetAssignmentWrapper {
	p.Name = name
	return p
}

// WithFlavors sets the Flavors of the PodSetAssignment.
func (p *PodSetAssignmentWrapper) WithFlavors(flavors ResourceAssignment) *PodSetAssignmentWrapper {
	p.Flavors = flavors
	return p
}

// WithStatus sets the Status of the PodSetAssignment.
func (p *PodSetAssignmentWrapper) WithStatus(status *Status) *PodSetAssignmentWrapper {
	p.Status = status
	return p
}

// WithRequests sets the Requests of the PodSetAssignment.
func (p *PodSetAssignmentWrapper) WithRequests(requests corev1.ResourceList) *PodSetAssignmentWrapper {
	p.Requests = requests
	return p
}

// WithCount sets the Count of the PodSetAssignment.
func (p *PodSetAssignmentWrapper) WithCount(count int32) *PodSetAssignmentWrapper {
	p.Count = count
	return p
}

// Obj returns the inner PodSetAssignment.
func (p *PodSetAssignmentWrapper) Obj() *PodSetAssignment {
	return &p.PodSetAssignment
}
