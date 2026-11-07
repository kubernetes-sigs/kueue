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

package jobframework

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type durationTestJob struct{ GenericJob }

func (*durationTestJob) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "RestartableJob"}
}

type restartableDurationTestJob struct{ durationTestJob }

func (*restartableDurationTestJob) RunDurationDrained(context.Context, client.Reader) (bool, error) {
	return true, nil
}

func TestIntegrationSupportsRunDuration(t *testing.T) {
	cases := map[string]struct {
		enabled     bool
		restartable bool
		noOwner     bool
		want        bool
	}{
		"registered external capability": {enabled: true, restartable: true, want: true},
		"disabled integration":           {restartable: true},
		"no capability":                  {enabled: true},
		"unowned workload":               {enabled: true, restartable: true, noOwner: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := NewIntegrationManager()
			callbacks := testIntegrationCallbacks
			callbacks.NewJob = func() GenericJob {
				if tc.restartable {
					return &restartableDurationTestJob{}
				}
				return &durationTestJob{}
			}
			if err := m.RegisterIntegration("example.test/restartable", callbacks); err != nil {
				t.Fatal(err)
			}
			if tc.enabled {
				m.EnableIntegration("example.test/restartable")
			}
			wl := &kueue.Workload{}
			if !tc.noOwner {
				wl.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "example.test/v1", Kind: "RestartableJob", Name: "job", UID: "uid", Controller: new(true),
				}}
			}
			if got := m.SupportsRunDuration(wl); got != tc.want {
				t.Errorf("supports duration = %v, want %v", got, tc.want)
			}
		})
	}
}
