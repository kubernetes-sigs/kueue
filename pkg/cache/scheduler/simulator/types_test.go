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

package simulator

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBuildScheduleOptions(t *testing.T) {
	wl1 := client.ObjectKey{Namespace: "ns", Name: "wl1"}
	wl2 := client.ObjectKey{Namespace: "ns", Name: "wl2"}
	wl1OtherNs := client.ObjectKey{Namespace: "other", Name: "wl1"}

	cases := map[string]struct {
		opts        []ScheduleOption
		wantOptions *ScheduleOptions
		wantErr     string
	}{
		"no options: ok": {
			wantOptions: &ScheduleOptions{},
		},
		"unique committed targets: ok": {
			opts:        []ScheduleOption{WithCommitedTargets([]client.ObjectKey{wl1, wl2})},
			wantOptions: &ScheduleOptions{CommittedTargets: []client.ObjectKey{wl1, wl2}},
		},
		"same name in different namespaces: ok": {
			opts:        []ScheduleOption{WithCommitedTargets([]client.ObjectKey{wl1, wl1OtherNs})},
			wantOptions: &ScheduleOptions{CommittedTargets: []client.ObjectKey{wl1, wl1OtherNs}},
		},
		"later option overrides earlier committed targets: ok": {
			opts: []ScheduleOption{
				WithCommitedTargets([]client.ObjectKey{wl1}),
				WithCommitedTargets([]client.ObjectKey{wl1, wl2}),
			},
			wantOptions: &ScheduleOptions{CommittedTargets: []client.ObjectKey{wl1, wl2}},
		},
		"duplicate committed target result in a validation error": {
			opts:    []ScheduleOption{WithCommitedTargets([]client.ObjectKey{wl1, wl2, wl1})},
			wantErr: "commited preemption target listed twice: ns/wl1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotOptions, err := BuildScheduleOptions(tc.opts)
			var gotErr string
			if err != nil {
				gotErr = err.Error()
			}
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantOptions, gotOptions); diff != "" {
				t.Errorf("Unexpected options (-want,+got):\n%s", diff)
			}
		})
	}
}
