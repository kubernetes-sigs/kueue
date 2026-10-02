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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
)

var (
	gvk = disaggregatedsetv1.SchemeGroupVersion.WithKind("DisaggregatedSet")
)

const (
	FrameworkName = "disaggregatedset.x-k8s.io/disaggregatedset"

	defaultReplicas = 1
	defaultSize     = 1
	defaultSlices   = 1
)

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

func CanSupportIntegration(opts ...jobframework.Option) (bool, error) {
	if !features.Enabled(features.DisaggregatedSetIntegration) {
		return false, fmt.Errorf("%s integration is alpha feature. please enable %s featuregate", FrameworkName, features.DisaggregatedSetIntegration)
	}
	return true, nil
}

type DisaggregatedSet disaggregatedsetv1.DisaggregatedSet

func fromObject(o runtime.Object) *DisaggregatedSet {
	return (*DisaggregatedSet)(o.(*disaggregatedsetv1.DisaggregatedSet))
}

func (ds *DisaggregatedSet) Object() client.Object {
	return (*disaggregatedsetv1.DisaggregatedSet)(ds)
}

func (ds *DisaggregatedSet) GVK() schema.GroupVersionKind {
	return gvk
}

func SetupIndexes(context.Context, client.FieldIndexer) error {
	return nil
}
