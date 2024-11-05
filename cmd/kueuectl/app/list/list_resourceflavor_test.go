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
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestResourceFlavorCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print resource flavor list": {
			objs: []runtime.Object{
				utiltesting.MakeResourceFlavor("rf1").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeResourceFlavor("rf2").
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   NODE LABELS   AGE
rf1                  60m
rf2                  120m
`,
		},
		"should print resource flavor list with node labels": {
			objs: []runtime.Object{
				utiltesting.MakeResourceFlavor("rf1").
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					NodeLabel("key1", "value1").
					NodeLabel("key2", "value2").
					Obj(),
				utiltesting.MakeResourceFlavor("rf2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					NodeLabel("key3", "value3").
					NodeLabel("key4", "value4").
					Obj(),
			},
			wantOut: `NAME   NODE LABELS                AGE
rf1    key1=value1, key2=value2   60m
rf2    key3=value3, key4=value4   120m
`,
		},
		"should print resource flavor list with label selector filter": {
			args: []string{"--selector", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeResourceFlavor("rf1").
					Label("key", "value1").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeResourceFlavor("rf2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   NODE LABELS   AGE
rf1                  60m
`,
		},
		"should print resource flavor list with label selector filter (short flag)": {
			args: []string{"-l", "foo=bar"},
			objs: []runtime.Object{
				utiltesting.MakeResourceFlavor("rf1").
					Label("foo", "bar").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeResourceFlavor("rf2").
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   NODE LABELS   AGE
rf1                  60m
`,
		},
		"should print not found error": {
			wantOutErr: "No resources found\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))

			cmd := NewResourceFlavorCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
			cmd.SetOut(out)
			cmd.SetErr(outErr)
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
