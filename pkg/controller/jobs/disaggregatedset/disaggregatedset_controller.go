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
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
)

var (
	gvk = disaggregatedsetv1.GroupVersion.WithKind("DisaggregatedSet")
)

const (
	FrameworkName = "disaggregatedset.x-k8s.io/disaggregatedset"
)

var errFeatureGateDisabled = errors.New("feature gate disabled")

func RegisterIntegration(m *jobframework.IntegrationManager) error {
	return m.RegisterIntegration(FrameworkName, jobframework.IntegrationCallbacks{
		SetupIndexes:                    SetupIndexes,
		NewReconciler:                   NewReconciler,
		SetupWebhook:                    SetupWebhook,
		JobType:                         &disaggregatedsetv1.DisaggregatedSet{},
		AddToScheme:                     disaggregatedsetv1.AddToScheme,
		CanSupportIntegration:           CanSupportIntegration,
		ImplicitlyEnabledFrameworkNames: []string{"pod"},
		GVK:                             gvk,
	})
}

func SetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}

func CanSupportIntegration(...jobframework.Option) (bool, error) {
	if !features.Enabled(features.DisaggregatedSetIntegration) {
		return false, fmt.Errorf("%w: %s integration is alpha feature. please enable %s featuregate", errFeatureGateDisabled, FrameworkName, features.DisaggregatedSetIntegration)
	}
	return true, nil
}
