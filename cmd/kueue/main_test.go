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

package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestSchemeConvertibleTypes(t *testing.T) {
	// The CRDs of these kinds use webhook conversion, and controller-runtime
	// registers /convert only when the manager scheme reports them as convertible.
	cases := []struct {
		name   string
		object runtime.Object
	}{
		{
			name:   "ClusterQueue",
			object: &kueue.ClusterQueue{},
		},
		{
			name:   "Cohort",
			object: &kueue.Cohort{},
		},
		{
			name:   "LocalQueue",
			object: &kueue.LocalQueue{},
		},
		{
			name:   "MultiKueueCluster",
			object: &kueue.MultiKueueCluster{},
		},
		{
			name:   "Workload",
			object: &kueue.Workload{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			convertible, err := conversion.IsConvertible(scheme, tc.object)
			if err != nil {
				t.Fatalf("conversion.IsConvertible() returned an error: %v", err)
			}
			if !convertible {
				t.Errorf("%s is not convertible in the manager scheme, so /convert would not be served", tc.name)
			}
		})
	}
}
