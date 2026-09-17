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

package dra

import (
	"net/http"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

var resourceSliceGVK = resourcev1.SchemeGroupVersion.WithKind("ResourceSlice")

func TestCheckResourceSliceAPIAvailable(t *testing.T) {
	cases := map[string]struct {
		partitionableDevicesFeatureGate bool
		consumableCapacityFeatureGate   bool
		ResourceSliceAvailable          bool
		want                            bool
	}{
		"no feature gates enabled": {
			partitionableDevicesFeatureGate: false,
			consumableCapacityFeatureGate:   false,
			ResourceSliceAvailable:          true,
			want:                            false,
		},
		"KueueDRAIntegrationPartitionableDevices enabled, API available": {
			partitionableDevicesFeatureGate: true,
			consumableCapacityFeatureGate:   false,
			ResourceSliceAvailable:          true,
			want:                            true,
		},
		"KueueDRAIntegrationConsumableCapacity enabled, API available": {
			partitionableDevicesFeatureGate: false,
			consumableCapacityFeatureGate:   true,
			ResourceSliceAvailable:          true,
			want:                            true,
		},
		"both feature gates enabled, API available": {
			partitionableDevicesFeatureGate: true,
			consumableCapacityFeatureGate:   true,
			ResourceSliceAvailable:          true,
			want:                            true,
		},
		"KueueDRAIntegrationPartitionableDevices enabled, API unavailable": {
			partitionableDevicesFeatureGate: true,
			consumableCapacityFeatureGate:   false,
			ResourceSliceAvailable:          false,
			want:                            false,
		},
		"KueueDRAIntegrationConsumableCapacity enabled, API unavailable": {
			partitionableDevicesFeatureGate: false,
			consumableCapacityFeatureGate:   true,
			ResourceSliceAvailable:          false,
			want:                            false,
		},
		"both feature gates enabled, API unavailable": {
			partitionableDevicesFeatureGate: true,
			consumableCapacityFeatureGate:   true,
			ResourceSliceAvailable:          false,
			want:                            false,
		},
	}
	k8sClient := utiltesting.NewClientBuilder().Build()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationPartitionableDevices, tc.partitionableDevicesFeatureGate)
			features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationConsumableCapacity, tc.consumableCapacityFeatureGate)

			mgr, err := ctrlmgr.New(&rest.Config{}, ctrlmgr.Options{
				Scheme: k8sClient.Scheme(),
				NewClient: func(*rest.Config, client.Options) (client.Client, error) {
					return k8sClient, nil
				},
				MapperProvider: func(*rest.Config, *http.Client) (apimeta.RESTMapper, error) {
					mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{resourceSliceGVK.GroupVersion()})
					if tc.ResourceSliceAvailable {
						mapper.Add(resourceSliceGVK, apimeta.RESTScopeRoot)
					}
					return mapper, nil
				},
			})
			if err != nil {
				t.Fatalf("failed to create manager: %v", err)
			}

			got := CheckResourceSliceAPIAvailable(mgr)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
