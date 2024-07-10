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

package list

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes/fake"
	testingclock "k8s.io/utils/clock/testing"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestListCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print job list with all namespaces": {
			args: []string{"job", "--all-namespaces"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					Profile("profile2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   PROFILE    COMPLETIONS   DURATION   AGE
ns1         j1     profile1   3/3           60m        60m
ns2         j2     profile2   3/3           60m        60m
`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			tcg.WithK8sClientset(fake.NewSimpleClientset(tc.objs...))

			cmd := NewListCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
