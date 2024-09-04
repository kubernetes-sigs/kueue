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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestSlurmCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print only kjobctl jobs": {
			ns: "ns1",
			objs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print only Slurm mode jobs": {
			ns: "ns1",
			objs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with namespace filter": {
			ns: "ns1",
			objs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with profile filter": {
			args: []string{"--profile", "profile1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with profile filter (short flag)": {
			args: []string{"-p", "profile1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with localqueue filter": {
			args: []string{"--localqueue", "lq1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with localqueue filter (short flag)": {
			args: []string{"-q", "lq1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with label selector filter": {
			args: []string{"--selector", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Label("foo", "bar").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with label selector filter (short flag)": {
			args: []string{"-l", "foo=bar"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Label("foo", "bar").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with field selector filter": {
			args: []string{"--field-selector", "metadata.name=j1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq2").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
j1     profile1   lq1           3/3           60m        60m
`,
		},
		"should print not found error": {
			wantOutErr: fmt.Sprintf("No resources found in %s namespace.\n", metav1.NamespaceDefault),
		},
		"should print not found error with all-namespaces filter": {
			args:       []string{"-A"},
			wantOutErr: "No resources found\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			clientset := fake.NewSimpleClientset(tc.objs...)
			clientset.PrependReactor("list", "jobs", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				listAction := action.(kubetesting.ListActionImpl)
				fieldsSelector := listAction.GetListRestrictions().Fields

				obj, err := clientset.Tracker().List(listAction.GetResource(), listAction.GetKind(), listAction.Namespace)
				jobList := obj.(*batchv1.JobList)

				filtered := make([]batchv1.Job, 0, len(jobList.Items))
				for _, item := range jobList.Items {
					fieldsSet := fields.Set{
						"metadata.name": item.Name,
					}
					if fieldsSelector.Matches(fieldsSet) {
						filtered = append(filtered, item)
					}
				}
				jobList.Items = filtered
				return true, jobList, err
			})

			tcg := cmdtesting.NewTestClientGetter().WithK8sClientset(clientset)
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewSlurmCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
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
