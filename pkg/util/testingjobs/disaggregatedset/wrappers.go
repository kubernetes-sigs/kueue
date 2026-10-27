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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
)

// DisaggregatedSetWrapper wraps a DisaggregatedSet.
type DisaggregatedSetWrapper struct {
	disaggregatedsetv1.DisaggregatedSet
}

// MakeDisaggregatedSet creates a wrapper for a DisaggregatedSet with one slice and no roles.
func MakeDisaggregatedSet(name, ns string) *DisaggregatedSetWrapper {
	return &DisaggregatedSetWrapper{disaggregatedsetv1.DisaggregatedSet{
		Name:      name,
		Namespace: ns,
		Spec: disaggregatedsetv1.DisaggregatedSetSpec{
			Slices: new(int32(1)),
		},
	}}
}

// Obj returns the inner DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Obj() *disaggregatedsetv1.DisaggregatedSet {
	return &w.DisaggregatedSet
}

// Clone returns a deep copy of the wrapper.
func (w *DisaggregatedSetWrapper) Clone() *DisaggregatedSetWrapper {
	return &DisaggregatedSetWrapper{DisaggregatedSet: *w.DeepCopy()}
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

// Queue sets the queue name of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Queue(q string) *DisaggregatedSetWrapper {
	return w.Label(constants.QueueLabel, q)
}

// WorkloadPriorityClass sets the WorkloadPriorityClass of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) WorkloadPriorityClass(wpc string) *DisaggregatedSetWrapper {
	return w.Label(constants.WorkloadPriorityClassLabel, wpc)
}

// UID sets the UID of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) UID(uid string) *DisaggregatedSetWrapper {
	w.ObjectMeta.UID = types.UID(uid)
	return w
}

// Slices sets the number of slices of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Slices(n int32) *DisaggregatedSetWrapper {
	w.Spec.Slices = &n
	return w
}

// Roles appends copies of roles to the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) Roles(roles ...*RoleWrapper) *DisaggregatedSetWrapper {
	for _, r := range roles {
		w.Spec.Roles = append(w.Spec.Roles, *r.Obj().DeepCopy())
	}
	return w
}

// PlacementPolicy sets the placement policy of the DisaggregatedSet.
func (w *DisaggregatedSetWrapper) PlacementPolicy(t disaggregatedsetv1.PlacementType, topology string) *DisaggregatedSetWrapper {
	w.Spec.PlacementPolicy = &disaggregatedsetv1.PlacementPolicy{Type: t, Topology: topology}
	return w
}

// RoleStatus sets the observed ready replicas of a role.
func (w *DisaggregatedSetWrapper) RoleStatus(name string, readyReplicas int32) *DisaggregatedSetWrapper {
	w.Status.RoleStatuses = append(w.Status.RoleStatuses, disaggregatedsetv1.RoleStatus{
		Name:          name,
		ReadyReplicas: readyReplicas,
	})
	return w
}

// RoleWrapper wraps a DisaggregatedSet role.
type RoleWrapper struct {
	disaggregatedsetv1.DisaggregatedRoleSpec
}

// MakeRole creates a wrapper for a role with one replica of size 1 and a single
// worker container.
func MakeRole(name string) *RoleWrapper {
	return &RoleWrapper{disaggregatedsetv1.DisaggregatedRoleSpec{
		Name: name,
		Spec: leaderworkersetv1.LeaderWorkerSetSpec{
			Replicas:        new(int32(1)),
			StartupPolicy:   leaderworkersetv1.LeaderCreatedStartupPolicy,
			RolloutStrategy: leaderworkersetv1.RolloutStrategy{Type: leaderworkersetv1.RollingUpdateStrategyType},
			LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
				WorkerTemplate: podTemplate(),
				Size:           new(int32(1)),
			},
		},
	}}
}

func podTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
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
	}
}

// Obj returns the inner role.
func (w *RoleWrapper) Obj() *disaggregatedsetv1.DisaggregatedRoleSpec {
	return &w.DisaggregatedRoleSpec
}

// Clone returns a deep copy of the wrapper.
func (w *RoleWrapper) Clone() *RoleWrapper {
	return &RoleWrapper{DisaggregatedRoleSpec: *w.DeepCopy()}
}

// Replicas sets the number of groups of the role per slice.
func (w *RoleWrapper) Replicas(n int32) *RoleWrapper {
	w.Spec.Replicas = &n
	return w
}

// Size sets the number of Pods in each group of the role.
func (w *RoleWrapper) Size(n int32) *RoleWrapper {
	w.Spec.LeaderWorkerTemplate.Size = &n
	return w
}

// WithLeader adds a leader template with a single container to the role.
func (w *RoleWrapper) WithLeader() *RoleWrapper {
	w.Spec.LeaderWorkerTemplate.LeaderTemplate = new(podTemplate())
	return w
}

// WithoutLeader removes the leader template of the role.
func (w *RoleWrapper) WithoutLeader() *RoleWrapper {
	w.Spec.LeaderWorkerTemplate.LeaderTemplate = nil
	return w
}

// Label sets a label in the role metadata.
func (w *RoleWrapper) Label(k, v string) *RoleWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string, 1)
	}
	w.Labels[k] = v
	return w
}

// Annotation sets an annotation in the role metadata.
func (w *RoleWrapper) Annotation(k, v string) *RoleWrapper {
	setKey(&w.Annotations, k, v)
	return w
}

// LeaderTemplateLabel sets a label on the leader template.
func (w *RoleWrapper) LeaderTemplateLabel(k, v string) *RoleWrapper {
	setKey(&w.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels, k, v)
	return w
}

// WorkerTemplateLabel sets a label on the worker template.
func (w *RoleWrapper) WorkerTemplateLabel(k, v string) *RoleWrapper {
	setKey(&w.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels, k, v)
	return w
}

// LeaderTemplateAnnotation sets an annotation on the leader template.
func (w *RoleWrapper) LeaderTemplateAnnotation(k, v string) *RoleWrapper {
	setKey(&w.Spec.LeaderWorkerTemplate.LeaderTemplate.Annotations, k, v)
	return w
}

// WorkerTemplateAnnotation sets an annotation on the worker template.
func (w *RoleWrapper) WorkerTemplateAnnotation(k, v string) *RoleWrapper {
	setKey(&w.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations, k, v)
	return w
}

// PodTemplateAnnotation sets an annotation on every template of the role.
func (w *RoleWrapper) PodTemplateAnnotation(k, v string) *RoleWrapper {
	for _, t := range w.templates() {
		setKey(&t.Annotations, k, v)
	}
	return w
}

// PodGroupServingAnnotation sets the pod group serving annotation on every
// template of the role.
func (w *RoleWrapper) PodGroupServingAnnotation() *RoleWrapper {
	return w.PodTemplateAnnotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue)
}

func setKey(m *map[string]string, k, v string) {
	if *m == nil {
		*m = make(map[string]string, 1)
	}
	(*m)[k] = v
}

// Image sets the image of the first container of every template of the role.
func (w *RoleWrapper) Image(image string) *RoleWrapper {
	for _, t := range w.templates() {
		t.Spec.Containers[0].Image = image
	}
	return w
}

// Env adds an environment variable to the first container of every template of the role.
func (w *RoleWrapper) Env(name, value string) *RoleWrapper {
	for _, t := range w.templates() {
		t.Spec.Containers[0].Env = append(t.Spec.Containers[0].Env, corev1.EnvVar{Name: name, Value: value})
	}
	return w
}

// Request sets a resource request on the first container of every template of the role.
func (w *RoleWrapper) Request(name corev1.ResourceName, quantity string) *RoleWrapper {
	for _, t := range w.templates() {
		t.Spec.Containers[0].Resources.Requests[name] = resource.MustParse(quantity)
	}
	return w
}

// RequestAndLimit sets a resource request and limit on the first container of
// every template of the role.
func (w *RoleWrapper) RequestAndLimit(name corev1.ResourceName, quantity string) *RoleWrapper {
	for _, t := range w.templates() {
		resources := &t.Spec.Containers[0].Resources
		resources.Requests[name] = resource.MustParse(quantity)
		if resources.Limits == nil {
			resources.Limits = corev1.ResourceList{}
		}
		resources.Limits[name] = resource.MustParse(quantity)
	}
	return w
}

// Args sets the arguments of the first container of every template of the role.
func (w *RoleWrapper) Args(args ...string) *RoleWrapper {
	for _, t := range w.templates() {
		t.Spec.Containers[0].Args = args
	}
	return w
}

// TerminationGracePeriod sets the termination grace period of every template
// of the role.
func (w *RoleWrapper) TerminationGracePeriod(seconds int64) *RoleWrapper {
	for _, t := range w.templates() {
		t.Spec.TerminationGracePeriodSeconds = &seconds
	}
	return w
}

// StartupPolicy sets the startup policy of the role.
func (w *RoleWrapper) StartupPolicy(p leaderworkersetv1.StartupPolicyType) *RoleWrapper {
	w.Spec.StartupPolicy = p
	return w
}

func (w *RoleWrapper) templates() []*corev1.PodTemplateSpec {
	templates := []*corev1.PodTemplateSpec{&w.Spec.LeaderWorkerTemplate.WorkerTemplate}
	if w.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
		templates = append(templates, w.Spec.LeaderWorkerTemplate.LeaderTemplate)
	}
	return templates
}

// MaxSurge sets the rollout maxSurge of the role.
func (w *RoleWrapper) MaxSurge(v intstr.IntOrString) *RoleWrapper {
	w.rollingUpdateConfiguration().MaxSurge = v
	return w
}

// MaxUnavailable sets the rollout maxUnavailable of the role.
func (w *RoleWrapper) MaxUnavailable(v intstr.IntOrString) *RoleWrapper {
	w.rollingUpdateConfiguration().MaxUnavailable = v
	return w
}

func (w *RoleWrapper) rollingUpdateConfiguration() *leaderworkersetv1.RollingUpdateConfiguration {
	if w.Spec.RolloutStrategy.RollingUpdateConfiguration == nil {
		w.Spec.RolloutStrategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{
			MaxUnavailable: intstr.FromInt32(1),
		}
	}
	return w.Spec.RolloutStrategy.RollingUpdateConfiguration
}

// ScalingMode sets the scaling mode of the role.
func (w *RoleWrapper) ScalingMode(mode disaggregatedsetv1.RoleScalingMode) *RoleWrapper {
	w.Scaling = &disaggregatedsetv1.RoleScaling{Mode: mode}
	return w
}

// SubRoles sets sub-roles with the given names on the role.
func (w *RoleWrapper) SubRoles(names ...string) *RoleWrapper {
	for _, name := range names {
		w.DisaggregatedRoleSpec.SubRoles = append(w.DisaggregatedRoleSpec.SubRoles, disaggregatedsetv1.DisaggregatedSubRoleSpec{Name: name})
	}
	return w
}

// Scheduling sets the LeaderWorkerSet scheduling of the role.
func (w *RoleWrapper) Scheduling(s *leaderworkersetv1.LeaderWorkerSetScheduling) *RoleWrapper {
	w.Spec.Scheduling = s
	return w
}

// GroupIdentity sets the group identity of the role.
func (w *RoleWrapper) GroupIdentity(g leaderworkersetv1.GroupIdentityType) *RoleWrapper {
	w.Spec.GroupIdentity = g
	return w
}

// MaxGroupRestarts sets the group restart budget of the role.
func (w *RoleWrapper) MaxGroupRestarts(n int32) *RoleWrapper {
	w.Spec.LeaderWorkerTemplate.MaxGroupRestarts = &n
	return w
}

// RestartBackoff sets the group restart backoff of the role.
func (w *RoleWrapper) RestartBackoff(baseSeconds, capSeconds int32) *RoleWrapper {
	w.Spec.LeaderWorkerTemplate.RestartBackoff = &leaderworkersetv1.RestartBackoff{
		BaseSeconds: &baseSeconds,
		CapSeconds:  &capSeconds,
	}
	return w
}
