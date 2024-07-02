/*
Copyright 2024 The Kubernetes Authors.

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

package create

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestCreateResourceFlavor(t *testing.T) {
	testCases := map[string]struct {
		options  *ResourceFlavorOptions
		expected *v1beta1.ResourceFlavor
	}{
		"success_create": {
			options: &ResourceFlavorOptions{
				Name: "rf",
			},
			expected: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: "kueue.x-k8s.io/v1beta1", Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec:       v1beta1.ResourceFlavorSpec{},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			rf := tc.options.createResourceFlavor()
			if diff := cmp.Diff(tc.expected, rf); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
