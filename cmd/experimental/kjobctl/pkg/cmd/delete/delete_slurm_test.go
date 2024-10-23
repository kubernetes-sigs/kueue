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

package delete

import (
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestSlurmCmd(t *testing.T) {
	testCases := map[string]struct {
		ns         string
		args       []string
		objs       []runtime.Object
		wantJobs   []batchv1.Job
		wantOut    string
		wantOutErr string
		wantErr    string
	}{
		"shouldn't delete slurm job because it is not found": {
			args: []string{"j"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOutErr: "jobs.batch \"j\" not found\n",
		},
		"shouldn't delete slurm job because it is not created via kjob": {
			args: []string{"j1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j1", metav1.NamespaceDefault).Obj(),
			},
			wantOutErr: "jobs.batch \"j1\" not created via kjob\n",
		},
		"shouldn't delete slurm job because it is not used for Job mode": {
			args: []string{"j1", "j2"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.JobMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Obj(),
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.JobMode).Obj(),
			},
			wantOutErr: `jobs.batch "j1" created in "" mode. Switch to the correct mode to delete it
jobs.batch "j2" created in "Job" mode. Switch to the correct mode to delete it
`,
		},
		"should delete slurm job": {
			args: []string{"j1"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOut: "job.batch/j1 deleted\n",
		},
		"should delete slurm jobs": {
			args: []string{"j1", "j2"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOut: "job.batch/j1 deleted\njob.batch/j2 deleted\n",
		},
		"should delete only one slurm job": {
			args: []string{"j1", "j"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOut:    "job.batch/j1 deleted\n",
			wantOutErr: "jobs.batch \"j\" not found\n",
		},
		"shouldn't delete slurm job with client dry run": {
			args: []string{"j1", "--dry-run", "client"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOut: "job.batch/j1 deleted (client dry run)\n",
		},
		"shouldn't delete slurm job with server dry run": {
			args: []string{"j1", "--dry-run", "server"},
			objs: []runtime.Object{
				wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantJobs: []batchv1.Job{
				*wrappers.MakeJob("j1", metav1.NamespaceDefault).Profile("p1").Mode(v1alpha1.SlurmMode).Obj(),
				*wrappers.MakeJob("j2", metav1.NamespaceDefault).Profile("p2").Mode(v1alpha1.SlurmMode).Obj(),
			},
			wantOut: "job.batch/j1 deleted (server dry run)\n",
		},
		"no args": {
			args:    []string{},
			wantErr: "requires at least 1 arg(s), only received 0",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ns := metav1.NamespaceDefault
			if tc.ns != "" {
				ns = tc.ns
			}

			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			clientset := k8sfake.NewSimpleClientset(tc.objs...)
			clientset.PrependReactor("delete", "jobs", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				if slices.Contains(action.(kubetesting.DeleteAction).GetDeleteOptions().DryRun, metav1.DryRunAll) {
					handled = true
				}
				return handled, ret, err
			})

			tcg := cmdtesting.NewTestClientGetter().
				WithK8sClientset(clientset).
				WithNamespace(ns)

			cmd := NewSlurmCmd(tcg, streams)
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if gotErr != nil {
				return
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected error output (-want/+got)\n%s", diff)
			}

			gotJobList, err := clientset.BatchV1().Jobs(tc.ns).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantJobs, gotJobList.Items); diff != "" {
				t.Errorf("Unexpected jobs (-want/+got)\n%s", diff)
			}
		})
	}
}
