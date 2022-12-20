/*
Copyright 2021 The Kubernetes Authors.

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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateClusterQueue(t *testing.T) {
	specField := field.NewPath("spec")
	resourceField := specField.Child("resources")

	testcases := []struct {
		name         string
		clusterQueue *kueue.ClusterQueue
		wantErr      field.ErrorList
	}{
		{
			name: "built-in resources with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Obj(),
			).Obj(),
		},
		{
			name: "invalid resource name",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("@cpu").Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceField.Index(0).Child("name"), "@cpu", ""),
			},
		},
		{
			name:         "in cohort",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Cohort("prod").Obj(),
		},
		{
			name:         "invalid cohort",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Cohort("@prod").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("cohort"), "@prod", ""),
			},
		},
		{
			name: "extended resources with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("example.com/gpu").Obj(),
			).Obj(),
		},
		{
			name: "extended resources with unqualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("example.com/@gpu").Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceField.Index(0).Child("name"), "example.com/@gpu", ""),
			},
		},
		{
			name: "flavor with qualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("x86", "10").Obj()).Obj(),
			).Obj(),
		},
		{
			name: "flavor with unqualified names",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("invalid_name", "10").Obj()).Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceField.Index(0).Child("flavors").Index(0).Child("name"), "invalid_name", ""),
			},
		},
		{
			name: "flavor quota with negative value",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("x86", "-1").Obj()).Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceField.Index(0).Child("flavors").Index(0).Child("quota", "min"), "-1", ""),
			},
		},
		{
			name: "flavor quota with zero value",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("x86", "0").Obj()).Obj(),
			).Obj(),
		},
		{
			name: "flavor quota with min is equal to max",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("x86", "1").Max("1").Obj()).Obj(),
			).Obj(),
		},
		{
			name: "flavor quota with min is greater than max",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Resource(
				testingutil.MakeResource("cpu").Flavor(testingutil.MakeFlavor("x86", "2").Max("1").Obj()).Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceField.Index(0).Child("flavors").Index(0).Child("quota", "min"), "2", ""),
			},
		},
		{
			name:         "empty queueing strategy is supported",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").Obj(),
		},
		{
			name: "namespaceSelector with invalid labels",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("namespaceSelector", "matchLabels"), "nospecialchars^=@", ""),
			},
		},
		{
			name: "namespaceSelector with invalid expressions",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "key",
						Operator: "In",
					},
				},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Required(specField.Child("namespaceSelector", "matchExpressions").Index(0).Child("values"), ""),
			},
		},
		{
			name: "multiple independent and codependent resources",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				Resource(testingutil.MakeResource("cpu").
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).Obj()).
				Resource(testingutil.MakeResource("memory").
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).Obj()).
				Resource(testingutil.MakeResource("example.com/gpu").
					Flavor(testingutil.MakeFlavor("gamma", "0").Obj()).
					Flavor(testingutil.MakeFlavor("omega", "0").Obj()).Obj()).
				Obj(),
		},
		{
			name: "multiple resources with matching flavors in different order",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				Resource(testingutil.MakeResource("cpu").
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).Obj()).
				Resource(testingutil.MakeResource("memory").
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("resources").Index(1).Child("flavors"), nil, ""),
			},
		},
		{
			name: "multiple resources with partial flavor match",
			clusterQueue: testingutil.MakeClusterQueue("cluster-queue").
				Resource(testingutil.MakeResource("cpu").
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).Obj()).
				Resource(testingutil.MakeResource("example.com/gpu").
					Flavor(testingutil.MakeFlavor("alpha", "0").Obj()).
					Flavor(testingutil.MakeFlavor("beta", "0").Obj()).
					Flavor(testingutil.MakeFlavor("omega", "0").Obj()).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("resources").Index(1).Child("flavors"), nil, ""),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateClusterQueue(tc.clusterQueue)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateClusterQueueUpdate(t *testing.T) {
	testcases := []struct {
		name            string
		newClusterQueue *kueue.ClusterQueue
		oldClusterQueue *kueue.ClusterQueue
		wantErr         field.ErrorList
	}{
		{
			name:            "queueingStrategy cannot be updated",
			newClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("StrictFIFO").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "queueingStrategy"), nil, ""),
			},
		},
		{
			name:            "same queueingStrategy",
			newClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			oldClusterQueue: testingutil.MakeClusterQueue("cluster-queue").QueueingStrategy("BestEffortFIFO").Obj(),
			wantErr:         nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateClusterQueueUpdate(tc.newClusterQueue, tc.oldClusterQueue)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
