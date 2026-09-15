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

package trainjob

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestSetupTrainJobWebhook_ValidatesCRD verifies that SetupTrainJobWebhook validates the CRD
func TestSetupTrainJobWebhook_ValidatesCRD(t *testing.T) {
	tests := []struct {
		name      string
		crd       *unstructured.Unstructured
		wantError bool
	}{
		{
			name:      "v2.2.0 compatible CRD should pass",
			crd:       createMockTrainJobCRDv22(),
			wantError: false,
		},
		{
			name:      "v2.1 incompatible CRD should fail",
			crd:       createMockTrainJobCRDv21(),
			wantError: true,
		},
		{
			name:      "missing CRD should NOT fail (graceful)",
			crd:       nil,
			wantError: false, // Changed from true to false - missing is OK
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object
			if tt.crd != nil {
				objects = []client.Object{tt.crd}
			}

			fakeClient := fake.NewClientBuilder().
				WithObjects(objects...).
				Build()

			ctx := t.Context()
			err := ValidateTrainJobCRDVersion(ctx, fakeClient)

			if (err != nil) != tt.wantError {
				t.Errorf("ValidateTrainJobCRDVersion() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
