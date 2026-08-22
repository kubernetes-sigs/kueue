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

package provisioning

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
)

func TestProvReqSyncedWithConfig(t *testing.T) {
	req := func(params map[string]autoscaling.Parameter) *autoscaling.ProvisioningRequest {
		return &autoscaling.ProvisioningRequest{Spec: autoscaling.ProvisioningRequestSpec{
			ProvisioningClassName: "queued", Parameters: params,
		}}
	}
	cases := map[string]struct {
		annotations map[string]string
		reqParams   map[string]autoscaling.Parameter
		cfgParams   map[string]kueue.Parameter
		want        bool
	}{
		"config and request agree": {
			reqParams: map[string]autoscaling.Parameter{"a": "1"},
			cfgParams: map[string]kueue.Parameter{"a": "1"},
			want:      true,
		},
		"config changed a value": {
			reqParams: map[string]autoscaling.Parameter{"a": "1"},
			cfgParams: map[string]kueue.Parameter{"a": "2"},
			want:      false,
		},
		"config dropped a parameter": {
			reqParams: map[string]autoscaling.Parameter{"a": "1", "b": "2"},
			cfgParams: map[string]kueue.Parameter{"a": "1"},
			want:      false,
		},
		"the workload put the extra parameter there": {
			annotations: map[string]string{constants.ProvReqAnnotationPrefix + "b": "2"},
			reqParams:   map[string]autoscaling.Parameter{"a": "1", "b": "2"},
			cfgParams:   map[string]kueue.Parameter{"a": "1"},
			want:        true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Annotations: tc.annotations}}
			prc := &kueue.ProvisioningRequestConfig{Spec: kueue.ProvisioningRequestConfigSpec{
				ProvisioningClassName: "queued", Parameters: tc.cfgParams,
			}}
			if got := provReqSyncedWithConfig(wl, req(tc.reqParams), prc); got != tc.want {
				t.Errorf("provReqSyncedWithConfig() = %v, want %v", got, tc.want)
			}
		})
	}
}
