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

package provisioning

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

var (
	ErrBadParametersRef = errors.New("bad parameters reference")
)

type storeHelper struct {
	client client.Client
}

func parametersRefValid(params *kueue.AdmissionCheckParametersReference) bool {
	if params == nil {
		return false
	}
	return params.Kind == ConfigKind && params.APIGroup == kueue.GroupVersion.Group && params.Name != ""
}

// FilterChecksForProvReq - returns a list of check names controlled by ProvisioningRequest.
func (c *storeHelper) FilterChecksForProvReq(ctx context.Context, states []kueue.AdmissionCheckState) ([]string, error) {
	var retActive []string
	for _, state := range states {
		ac := &kueue.AdmissionCheck{}

		if err := c.client.Get(ctx, types.NamespacedName{Name: state.Name}, ac); client.IgnoreNotFound(err) != nil {
			return nil, err
		} else if err == nil && ac.Spec.ControllerName == ControllerName {
			retActive = append(retActive, ac.Name)
		}
	}
	return retActive, nil
}

// ProvReqConfigForAdmissionCheck - get the config used by the check identified by the checks name,
// nil is return if the config is missing or improperly configured (the check is not active).
func (c *storeHelper) ProvReqConfigForAdmissionCheck(ctx context.Context, checkName string) (*kueue.ProvisioningRequestConfig, error) {
	ac := &kueue.AdmissionCheck{}
	if err := c.client.Get(ctx, types.NamespacedName{Name: checkName}, ac); err != nil {
		return nil, err
	}

	if !parametersRefValid(ac.Spec.Parameters) {
		return nil, ErrBadParametersRef
	}

	return c.ProvReqConfig(ctx, ac.Spec.Parameters.Name)
}

// ProvReqConfig - returns the config identified by its name
func (c *storeHelper) ProvReqConfig(ctx context.Context, name string) (*kueue.ProvisioningRequestConfig, error) {
	cfg := &kueue.ProvisioningRequestConfig{}
	if err := c.client.Get(ctx, types.NamespacedName{Name: name}, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// AdmissionChecksUsingProvReqConfig - returns a list containing the names of the checks using
// the provided config.
func (c *storeHelper) AdmissionChecksUsingProvReqConfig(ctx context.Context, name string) ([]string, error) {
	list := &kueue.AdmissionCheckList{}
	if err := c.client.List(ctx, list, client.MatchingFields{AdmissionCheckUsingConfigKey: name}); client.IgnoreNotFound(err) != nil {
		return nil, err
	}
	return slices.Map(list.Items, func(ac *kueue.AdmissionCheck) string { return ac.Name }), nil
}
