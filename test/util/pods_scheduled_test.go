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

package util

import (
	"testing"
)

func TestLabelsFromSelector(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		selector string
		want     map[string]string
		wantErr  bool
	}{
		"single key": {
			selector: "app=foo",
			want:     map[string]string{"app": "foo"},
		},
		"multiple keys": {
			selector: "a=1,b=2",
			want:     map[string]string{"a": "1", "b": "2"},
		},
		"duplicate same value": {
			selector: "app=foo,app=foo",
			want:     map[string]string{"app": "foo"},
		},
		"empty selector": {
			selector: "",
			wantErr:  true,
		},
		"conflicting duplicate key": {
			selector: "role=a,role=b",
			wantErr:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := LabelsFromSelector(tc.selector)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LabelsFromSelector() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("LabelsFromSelector() = %v, want %v", got, tc.want)
			}
			for k, wantVal := range tc.want {
				if got[k] != wantVal {
					t.Fatalf("LabelsFromSelector()[%q] = %q, want %q", k, got[k], wantVal)
				}
			}
		})
	}
}
