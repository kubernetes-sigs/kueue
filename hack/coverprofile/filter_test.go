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

package coverprofile

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFilter(t *testing.T) {
	const (
		mode       = "mode: count\n"
		scheduler  = "sigs.k8s.io/kueue/pkg/scheduler/scheduler.go:10.2,12.16 2 1\n"
		testingPkg = "sigs.k8s.io/kueue/pkg/util/testing/wrappers.go:3.1,4.2 1 1\n"
		latest     = "sigs.k8s.io/kueue/pkg/latest/latest.go:1.2,3.4 1 1\n"
		contest    = "sigs.k8s.io/kueue/pkg/contest/contest.go:1.2,3.4 1 0\n"
		behavioral = "sigs.k8s.io/kueue/test/util/behavioral/helpers.go:1.1,2.2 1 0\n"
		multikueue = "sigs.k8s.io/kueue/test/performance/multikueue/runner.go:5.1,6.2 1 1\n"
		framework  = "sigs.k8s.io/kueue/test/performance/framework/controllers/controllers.go:8.1,9.2 1 0\n"
		relative   = "test/util/foo.go:1.1,2.2 1 0\n"
	)

	tests := map[string]struct {
		in      string
		want    string
		wantErr string
	}{
		"drops test directories and keeps product paths": {
			in:   mode + scheduler + testingPkg + latest + contest + behavioral + multikueue + framework + relative,
			want: mode + scheduler + testingPkg + latest + contest,
		},
		"backslash test segment is dropped and testing package is kept": {
			in:   mode + `sigs.k8s.io\kueue\test\util\foo.go:1.1,2.2 1 0` + "\n" + `sigs.k8s.io\kueue\pkg\util\testing\wrappers.go:3.1,4.2 1 1` + "\n",
			want: mode + "sigs.k8s.io\\kueue\\pkg\\util\\testing\\wrappers.go:3.1,4.2 1 1\n",
		},
		"mode line only": {
			in:   "mode: set\n",
			want: "mode: set\n",
		},
		"malformed line": {
			in:      mode + "not a block\n",
			wantErr: `malformed coverage profile line: "not a block"`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Filter(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Filter() error = nil, want %q", tc.wantErr)
				}
				if diff := cmp.Diff(tc.wantErr, err.Error()); diff != "" {
					t.Fatalf("Filter() error mismatch (-want +got):\n%s", diff)
				}
				return
			}
			if err != nil {
				t.Fatalf("Filter() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Filter() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
