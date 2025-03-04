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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/features"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateCohort(t *testing.T) {
	specPath := field.NewPath("spec")
	resourceGroupsPath := specPath.Child("resourceGroups")

	testcases := []struct {
		name                string
		cohort              *kueue.Cohort
		wantErr             field.ErrorList
		disableLendingLimit bool
	}{
		{
			name: "flavor quota with lendingLimit and empty parent",
			cohort: testingutil.MakeCohort("cohort").
				ResourceGroup(
					*testingutil.MakeFlavorQuotas("x86").Resource("cpu", "1", "", "1").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(resourceGroupsPath.Index(0).Child("flavors").Index(0).Child("resources").Index(0).Child("lendingLimit"), "1", "must be nil when parent is empty"),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			gotErr := validateCohort(tc.cohort)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue")); diff != "" {
				t.Errorf("ValidateResources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
