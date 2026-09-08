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

package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// DynamicQuotaOrchestratorWrapper wraps a DynamicQuotaOrchestrator.
type DynamicQuotaOrchestratorWrapper struct {
	kueuealpha.DynamicQuotaOrchestrator
}

// MakeDynamicQuotaOrchestrator creates a DynamicQuotaOrchestrator wrapper.
func MakeDynamicQuotaOrchestrator(name string) *DynamicQuotaOrchestratorWrapper {
	return &DynamicQuotaOrchestratorWrapper{
		DynamicQuotaOrchestrator: kueuealpha.DynamicQuotaOrchestrator{
			TypeMeta: metav1.TypeMeta{
				APIVersion: kueuealpha.SchemeGroupVersion.String(),
				Kind:       "DynamicQuotaOrchestrator",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

// Obj returns the DynamicQuotaOrchestrator.
func (w *DynamicQuotaOrchestratorWrapper) Obj() *kueuealpha.DynamicQuotaOrchestrator {
	return &w.DynamicQuotaOrchestrator
}

// DiscoveryProvider adds a CapacityDiscoveryProviderContribution to the DynamicQuotaOrchestrator.
func (w *DynamicQuotaOrchestratorWrapper) DiscoveryProvider(name string, multiplier *resource.Quantity) *DynamicQuotaOrchestratorWrapper {
	w.Spec.CapacityDiscovery.Providers = append(w.Spec.CapacityDiscovery.Providers, kueuealpha.CapacityDiscoveryProviderContribution{
		Name:                        kueuealpha.CapacityProviderName(name),
		EffectiveCapacityMultiplier: multiplier,
	})
	return w
}

// SubtreeRoot sets the subtree root of the DynamicQuotaOrchestrator.
func (w *DynamicQuotaOrchestratorWrapper) SubtreeRoot(kind kueuealpha.SubtreeRootRefKind, name string) *DynamicQuotaOrchestratorWrapper {
	w.Spec.CapacityDistribution = &kueuealpha.CapacityDistribution{
		SubtreeRootQuotaRef: kueuealpha.CapacityDistributionSubtreeRootRef{
			Kind: kind,
			Name: name,
		},
	}
	return w
}

// EffectiveCapacity sets the effective capacity in status.
func (w *DynamicQuotaOrchestratorWrapper) EffectiveCapacity(capacity *kueuealpha.EffectiveCapacity) *DynamicQuotaOrchestratorWrapper {
	w.Status.EffectiveCapacity = capacity
	return w
}

// Condition adds a condition to the DynamicQuotaOrchestrator.
func (w *DynamicQuotaOrchestratorWrapper) Condition(condition metav1.Condition) *DynamicQuotaOrchestratorWrapper {
	w.Status.Conditions = append(w.Status.Conditions, condition)
	return w
}

// Creation sets the creation timestamp of the DynamicQuotaOrchestrator.
func (w *DynamicQuotaOrchestratorWrapper) Creation(t time.Time) *DynamicQuotaOrchestratorWrapper {
	w.CreationTimestamp = metav1.NewTime(t)
	return w
}

// CapacityProviderWrapper wraps a CapacityProvider.
type CapacityProviderWrapper struct {
	kueuealpha.CapacityProvider
}

// MakeCapacityProvider creates a CapacityProvider wrapper.
func MakeCapacityProvider(name string) *CapacityProviderWrapper {
	return &CapacityProviderWrapper{
		CapacityProvider: kueuealpha.CapacityProvider{
			TypeMeta: metav1.TypeMeta{
				APIVersion: kueuealpha.SchemeGroupVersion.String(),
				Kind:       "CapacityProvider",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

// MakeCapacityProviderWithGenerateName creates a CapacityProvider wrapper with a generateName prefix.
func MakeCapacityProviderWithGenerateName(prefix string) *CapacityProviderWrapper {
	return MakeCapacityProvider("").GenerateName(prefix)
}

// GenerateName sets the generateName in the CapacityProvider.
func (w *CapacityProviderWrapper) GenerateName(prefix string) *CapacityProviderWrapper {
	w.Name = ""
	w.ObjectMeta.GenerateName = prefix
	return w
}

// Obj returns the CapacityProvider.
func (w *CapacityProviderWrapper) Obj() *kueuealpha.CapacityProvider {
	return &w.CapacityProvider
}

// ControllerName sets the controllerName in the CapacityProvider.
func (w *CapacityProviderWrapper) ControllerName(controllerName kueuealpha.CapacityProviderControllerName) *CapacityProviderWrapper {
	w.Spec.ControllerName = controllerName
	return w
}

// OrchestratedFlavors sets the orchestratedFlavors in the CapacityProvider.
func (w *CapacityProviderWrapper) OrchestratedFlavors(flavors ...string) *CapacityProviderWrapper {
	w.Spec.OrchestratedFlavors = make([]kueuealpha.CapacityProviderOrchestratedFlavor, len(flavors))
	for i, f := range flavors {
		w.Spec.OrchestratedFlavors[i] = kueuealpha.CapacityProviderOrchestratedFlavor{
			Name: kueuealpha.ResourceFlavorReference(f),
		}
	}
	return w
}

// Parameters sets the parameters reference in the CapacityProvider.
func (w *CapacityProviderWrapper) Parameters(apiGroup, kind, name string) *CapacityProviderWrapper {
	w.Spec.Parameters = &kueuealpha.CapacityProviderParametersReference{
		APIGroup: apiGroup,
		Kind:     kind,
		Name:     name,
	}
	return w
}

// Capacity sets the capacity in the CapacityProvider status.
func (w *CapacityProviderWrapper) Capacity(capacity *kueuealpha.CapacityProviderNormalizedCapacity) *CapacityProviderWrapper {
	w.Status.Capacity = capacity
	return w
}

// Condition adds a condition to the CapacityProvider.
func (w *CapacityProviderWrapper) Condition(condition metav1.Condition) *CapacityProviderWrapper {
	w.Status.Conditions = append(w.Status.Conditions, condition)
	return w
}

// CapacityProviderNormalizedCapacityWrapper wraps a CapacityProviderNormalizedCapacity.
type CapacityProviderNormalizedCapacityWrapper struct {
	kueuealpha.CapacityProviderNormalizedCapacity
}

// MakeNormalizedCapacity creates a CapacityProviderNormalizedCapacity wrapper.
func MakeNormalizedCapacity() *CapacityProviderNormalizedCapacityWrapper {
	return &CapacityProviderNormalizedCapacityWrapper{}
}

// Flavors appends flavors to the normalized capacity.
func (w *CapacityProviderNormalizedCapacityWrapper) Flavors(flavors ...kueuealpha.CapacityProviderNormalizedCapacityFlavor) *CapacityProviderNormalizedCapacityWrapper {
	w.CapacityProviderNormalizedCapacity.Flavors = append(w.CapacityProviderNormalizedCapacity.Flavors, flavors...)
	return w
}

// Obj returns the CapacityProviderNormalizedCapacity.
func (w *CapacityProviderNormalizedCapacityWrapper) Obj() *kueuealpha.CapacityProviderNormalizedCapacity {
	return &w.CapacityProviderNormalizedCapacity
}

// CapacityProviderNormalizedCapacityFlavorWrapper wraps a CapacityProviderNormalizedCapacityFlavor.
type CapacityProviderNormalizedCapacityFlavorWrapper struct {
	kueuealpha.CapacityProviderNormalizedCapacityFlavor
}

// MakeNormalizedCapacityFlavor creates a CapacityProviderNormalizedCapacityFlavor wrapper.
func MakeNormalizedCapacityFlavor(name string) *CapacityProviderNormalizedCapacityFlavorWrapper {
	return &CapacityProviderNormalizedCapacityFlavorWrapper{
		CapacityProviderNormalizedCapacityFlavor: kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			Name:      kueuealpha.ResourceFlavorReference(name),
			Resources: corev1.ResourceList{},
		},
	}
}

// Resource adds a resource quantity to the flavor.
func (f *CapacityProviderNormalizedCapacityFlavorWrapper) Resource(name corev1.ResourceName, qty string) *CapacityProviderNormalizedCapacityFlavorWrapper {
	if f.Resources == nil {
		f.Resources = corev1.ResourceList{}
	}
	f.Resources[name] = resource.MustParse(qty)
	return f
}

// Obj returns the inner CapacityProviderNormalizedCapacityFlavor.
func (f *CapacityProviderNormalizedCapacityFlavorWrapper) Obj() kueuealpha.CapacityProviderNormalizedCapacityFlavor {
	return f.CapacityProviderNormalizedCapacityFlavor
}

// EffectiveCapacityWrapper wraps an EffectiveCapacity.
type EffectiveCapacityWrapper struct {
	kueuealpha.EffectiveCapacity
}

// MakeEffectiveCapacity creates an EffectiveCapacity wrapper.
func MakeEffectiveCapacity() *EffectiveCapacityWrapper {
	return &EffectiveCapacityWrapper{}
}

// Flavors appends flavors to the effective capacity.
func (w *EffectiveCapacityWrapper) Flavors(flavors ...kueuealpha.EffectiveCapacityFlavor) *EffectiveCapacityWrapper {
	w.EffectiveCapacity.Flavors = append(w.EffectiveCapacity.Flavors, flavors...)
	return w
}

// Obj returns the EffectiveCapacity.
func (w *EffectiveCapacityWrapper) Obj() *kueuealpha.EffectiveCapacity {
	return &w.EffectiveCapacity
}

// EffectiveCapacityFlavorWrapper wraps an EffectiveCapacityFlavor.
type EffectiveCapacityFlavorWrapper struct {
	kueuealpha.EffectiveCapacityFlavor
}

// MakeEffectiveCapacityFlavor creates an EffectiveCapacityFlavor wrapper.
func MakeEffectiveCapacityFlavor(name string) *EffectiveCapacityFlavorWrapper {
	return &EffectiveCapacityFlavorWrapper{
		EffectiveCapacityFlavor: kueuealpha.EffectiveCapacityFlavor{
			Name:      kueuealpha.ResourceFlavorReference(name),
			Resources: corev1.ResourceList{},
		},
	}
}

// Resource adds a resource quantity to the flavor.
func (f *EffectiveCapacityFlavorWrapper) Resource(name corev1.ResourceName, qty string) *EffectiveCapacityFlavorWrapper {
	if f.Resources == nil {
		f.Resources = corev1.ResourceList{}
	}
	f.Resources[name] = resource.MustParse(qty)
	return f
}

// Obj returns the inner EffectiveCapacityFlavor.
func (f *EffectiveCapacityFlavorWrapper) Obj() *kueuealpha.EffectiveCapacityFlavor {
	return &f.EffectiveCapacityFlavor
}
