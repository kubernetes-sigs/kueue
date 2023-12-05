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

package admissioncheck

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

var (
	ErrNilParametersRef = errors.New("missing parameters reference")
	ErrBadParametersRef = errors.New("bad parameters reference")
)

type objAsPtr[T any] interface {
	client.Object
	*T
}

type ConfigHelper[PtrConfT objAsPtr[ConfT], ConfT any] struct {
	gk     schema.GroupKind
	client client.Client
}

func NewConfigHelper[PtrConfT objAsPtr[ConfT], ConfT any](c client.Client) (*ConfigHelper[PtrConfT, ConfT], error) {
	helper := ConfigHelper[PtrConfT, ConfT]{
		client: c,
	}

	gvk, err := apiutil.GVKForObject(helper.newConfigPtr(), c.Scheme())
	if err != nil {
		return nil, err
	}

	helper.gk = gvk.GroupKind()
	return &helper, nil
}

func (ch *ConfigHelper[PtrConfT, ConfT]) newConfigPtr() PtrConfT {
	return PtrConfT(new(ConfT))
}

// IsValidConfigReference - checks if the provided reference is addressing an object specific to this helper.
func (ch *ConfigHelper[PtrConfT, ConfT]) IsValidConfigReference(ref *kueue.AdmissionCheckParametersReference) (bool, error) {
	return refValidForGK(ref, ch.gk)
}

// ConfigByName - get the config identified by its name
func (ch *ConfigHelper[PtrConfT, ConfT]) ConfigByName(ctx context.Context, name string) (PtrConfT, error) {
	configPtr := ch.newConfigPtr()
	if err := ch.client.Get(ctx, types.NamespacedName{Name: name}, configPtr); err != nil {
		return nil, err
	}
	return configPtr, nil
}

// ConfigFromRef - get the config identified by ref if valid.
func (ch *ConfigHelper[PtrConfT, ConfT]) ConfigFromRef(ctx context.Context, ref *kueue.AdmissionCheckParametersReference) (PtrConfT, error) {
	if isValid, err := ch.IsValidConfigReference(ref); !isValid {
		return nil, err
	}
	return ch.ConfigByName(ctx, ref.Name)
}

// ConfigForAdmissionCheck - get the configuration of the admission check identified by its name if it uses the
// helpers configuration type.
func (ch *ConfigHelper[PtrConfT, ConfT]) ConfigForAdmissionCheck(ctx context.Context, checkName string) (PtrConfT, error) {
	ac := &kueue.AdmissionCheck{}
	if err := ch.client.Get(ctx, types.NamespacedName{Name: checkName}, ac); err != nil {
		return nil, err
	}

	return ch.ConfigFromRef(ctx, ac.Spec.Parameters)
}

func refValidForGK(ref *kueue.AdmissionCheckParametersReference, gk schema.GroupKind) (bool, error) {
	if ref == nil {
		return false, ErrNilParametersRef
	}

	if len(ref.Name) == 0 {
		return false, fmt.Errorf("empty name: %w", ErrBadParametersRef)
	}
	if gk.Group != ref.APIGroup {
		return false, fmt.Errorf("wrong group %q, expecting %q: %w", ref.APIGroup, gk.Group, ErrBadParametersRef)
	}

	if gk.Kind != ref.Kind {
		return false, fmt.Errorf("wrong kind %q, expecting %q: %w", ref.Kind, gk.Kind, ErrBadParametersRef)
	}
	return true, nil
}

func IndexerByConfigFunction(ControllerName string, gvk schema.GroupVersionKind) client.IndexerFunc {
	gk := gvk.GroupKind()
	return func(obj client.Object) []string {
		ac, isAc := obj.(*kueue.AdmissionCheck)
		if !isAc || ac == nil || ac.Spec.ControllerName != ControllerName {
			return nil
		}
		if isvalid, _ := refValidForGK(ac.Spec.Parameters, gk); !isvalid {
			return nil
		}
		return []string{ac.Spec.Parameters.Name}
	}
}

// FilterForController - returns a list of check names controlled by ControllerName.
func FilterForController(ctx context.Context, c client.Client, states []kueue.AdmissionCheckState, ControllerName string) ([]string, error) {
	var retActive []string
	for _, state := range states {
		ac := &kueue.AdmissionCheck{}

		if err := c.Get(ctx, types.NamespacedName{Name: state.Name}, ac); client.IgnoreNotFound(err) != nil {
			return nil, err
		} else if err == nil && ac.Spec.ControllerName == ControllerName {
			retActive = append(retActive, ac.Name)
		}
	}
	return retActive, nil
}
