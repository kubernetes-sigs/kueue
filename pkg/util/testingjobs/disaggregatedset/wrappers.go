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

package disaggregatedset

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
)

// DisaggregatedSetWrapper wraps a DisaggregatedSet.
type DisaggregatedSetWrapper struct {
	disaggregatedsetv1.DisaggregatedSet
}

// MakeDisaggregatedSet creates a wrapper for a DisaggregatedSet with no roles.
// Use Role() or RoleWithLeader() to add roles.
func MakeDisaggregatedSet(name, ns string) *DisaggregatedSetWrapper {
	return &DisaggregatedSetWrapper{disaggregatedsetv1.DisaggregatedSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: disaggregatedsetv1.DisaggregatedSetSpec{
			Roles: []disaggregatedsetv1.DisaggregatedRoleSpec{},
		},
	}}
}

// Obj returns the inner DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Obj() *disaggregatedsetv1.DisaggregatedSet {
	return &w.DisaggregatedSet
}

// Label sets a label on the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Label(k, v string) *DisaggregatedSetWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[k] = v
	return w
}

// Annotation sets an annotation on the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Annotation(k, v string) *DisaggregatedSetWrapper {
	if w.Annotations == nil {
		w.Annotations = make(map[string]string, 1)
	}
	w.Annotations[k] = v
	return w
}

// Queue updates the queue name of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Queue(q string) *DisaggregatedSetWrapper {
	return w.Label(constants.QueueLabel, q)
}

// UID sets the UID of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) UID(uid string) *DisaggregatedSetWrapper {
	w.ObjectMeta.UID = types.UID(uid)
	return w
}

// Slices sets the number of slices.
func (w *DisaggregatedSetWrapper) Slices(n int32) *DisaggregatedSetWrapper {
	w.Spec.Slices = &n
	return w
}

// Role adds a role with a worker-only template (no leader template).
func (w *DisaggregatedSetWrapper) Role(name string, replicas, size int32) *DisaggregatedSetWrapper {
	w.Spec.Roles = append(w.Spec.Roles, disaggregatedsetv1.DisaggregatedRoleSpec{
		Name: name,
		LeaderWorkerSetTemplateSpec: leaderworkersetv1.LeaderWorkerSetTemplateSpec{
			Spec: leaderworkersetv1.LeaderWorkerSetSpec{
				Replicas:      &replicas,
				StartupPolicy: leaderworkersetv1.LeaderCreatedStartupPolicy,
				RolloutStrategy: leaderworkersetv1.RolloutStrategy{
					Type: leaderworkersetv1.RollingUpdateStrategyType,
				},
				LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
					WorkerTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "c",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
					Size: &size,
				},
			},
		},
	})
	return w
}

// RoleWithLeader adds a role with both leader and worker templates.
func (w *DisaggregatedSetWrapper) RoleWithLeader(name string, replicas, size int32) *DisaggregatedSetWrapper {
	w.Spec.Roles = append(w.Spec.Roles, disaggregatedsetv1.DisaggregatedRoleSpec{
		Name: name,
		LeaderWorkerSetTemplateSpec: leaderworkersetv1.LeaderWorkerSetTemplateSpec{
			Spec: leaderworkersetv1.LeaderWorkerSetSpec{
				Replicas:      &replicas,
				StartupPolicy: leaderworkersetv1.LeaderCreatedStartupPolicy,
				RolloutStrategy: leaderworkersetv1.RolloutStrategy{
					Type: leaderworkersetv1.RollingUpdateStrategyType,
				},
				LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
					LeaderTemplate: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "leader",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
					WorkerTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:      "worker",
									Image:     utiltestingjobs.TestDefaultContainerImage,
									Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
								},
							},
							NodeSelector: map[string]string{},
						},
					},
					Size: &size,
				},
			},
		},
	})
	return w
}

// RoleRequest adds a resource request to the last-added role's worker container.
func (w *DisaggregatedSetWrapper) RoleRequest(r corev1.ResourceName, v string) *DisaggregatedSetWrapper {
	role := &w.Spec.Roles[len(w.Spec.Roles)-1]
	c := &role.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	c.Resources.Requests[r] = resource.MustParse(v)
	return w
}

// WorkloadPriorityClass sets the workload priority class label.
func (w *DisaggregatedSetWrapper) WorkloadPriorityClass(wpc string) *DisaggregatedSetWrapper {
	return w.Label(constants.WorkloadPriorityClassLabel, wpc)
}

// RoleStatus adds a role status entry.
func (w *DisaggregatedSetWrapper) RoleStatus(name string, replicas, readyReplicas int32) *DisaggregatedSetWrapper {
	w.Status.RoleStatuses = append(w.Status.RoleStatuses, disaggregatedsetv1.RoleStatus{
		Name:          name,
		Replicas:      replicas,
		ReadyReplicas: readyReplicas,
	})
	return w
}

// Image sets the container image and args for all roles' worker containers.
func (w *DisaggregatedSetWrapper) Image(image string, args []string) *DisaggregatedSetWrapper {
	for i := range w.Spec.Roles {
		role := &w.Spec.Roles[i]
		role.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = image
		role.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Args = args
		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			role.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Image = image
			role.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Args = args
		}
	}
	return w
}

// RequestAndLimit adds a resource request and limit to all roles' worker containers.
func (w *DisaggregatedSetWrapper) RequestAndLimit(r corev1.ResourceName, v string) *DisaggregatedSetWrapper {
	for i := range w.Spec.Roles {
		role := &w.Spec.Roles[i]
		c := &role.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
		if c.Resources.Requests == nil {
			c.Resources.Requests = corev1.ResourceList{}
		}
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		c.Resources.Requests[r] = resource.MustParse(v)
		c.Resources.Limits[r] = resource.MustParse(v)
		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			lc := &role.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0]
			if lc.Resources.Requests == nil {
				lc.Resources.Requests = corev1.ResourceList{}
			}
			if lc.Resources.Limits == nil {
				lc.Resources.Limits = corev1.ResourceList{}
			}
			lc.Resources.Requests[r] = resource.MustParse(v)
			lc.Resources.Limits[r] = resource.MustParse(v)
		}
	}
	return w
}

// TerminationGracePeriod sets the termination grace period for all roles.
func (w *DisaggregatedSetWrapper) TerminationGracePeriod(seconds int64) *DisaggregatedSetWrapper {
	for i := range w.Spec.Roles {
		role := &w.Spec.Roles[i]
		role.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.TerminationGracePeriodSeconds = &seconds
		if role.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
			role.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.TerminationGracePeriodSeconds = &seconds
		}
	}
	return w
}
