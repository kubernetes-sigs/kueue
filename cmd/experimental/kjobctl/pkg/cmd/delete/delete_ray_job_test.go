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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	kubetesting "k8s.io/client-go/testing"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestRayJobCmd(t *testing.T) {
	testCases := map[string]struct {
		ns          string
		args        []string
		objs        []runtime.Object
		wantRayJobs []rayv1.RayJob
		wantOut     string
		wantOutErr  string
		wantErr     string
	}{
		"shouldn't delete ray job because it is not found": {
			args: []string{"rj"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantRayJobs: []rayv1.RayJob{
				*wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				*wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOutErr: "rayjobs.ray.io \"rj\" not found\n",
		},
		"should delete ray job": {
			args: []string{"rj1"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantRayJobs: []rayv1.RayJob{
				*wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOut: "rayjob.ray.io/rj1 deleted\n",
		},
		"should delete ray jobs": {
			args: []string{"rj1", "rj2"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOut: "rayjob.ray.io/rj1 deleted\nrayjob.ray.io/rj2 deleted\n",
		},
		"should delete only one ray job": {
			args: []string{"rj1", "rj"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantRayJobs: []rayv1.RayJob{
				*wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOut:    "rayjob.ray.io/rj1 deleted\n",
			wantOutErr: "rayjobs.ray.io \"rj\" not found\n",
		},
		"shouldn't delete ray job with client dry run": {
			args: []string{"rj1", "--dry-run", "client"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantRayJobs: []rayv1.RayJob{
				*wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				*wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOut: "rayjob.ray.io/rj1 deleted (client dry run)\n",
		},
		"shouldn't delete ray job with server dry run": {
			args: []string{"rj1", "--dry-run", "server"},
			objs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantRayJobs: []rayv1.RayJob{
				*wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).Profile("p1").Obj(),
				*wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).Profile("p2").Obj(),
			},
			wantOut: "rayjob.ray.io/rj1 deleted (server dry run)\n",
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

			clientset := fake.NewSimpleClientset(tc.objs...)
			clientset.PrependReactor("delete", "rayjobs", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				if slices.Contains(action.(kubetesting.DeleteAction).GetDeleteOptions().DryRun, metav1.DryRunAll) {
					handled = true
				}
				return handled, ret, err
			})

			tcg := cmdtesting.NewTestClientGetter().
				WithRayClientset(clientset).
				WithNamespace(ns)

			cmd := NewRayJobCmd(tcg, streams)
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

			gotRayJobList, err := clientset.RayV1().RayJobs(tc.ns).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantRayJobs, gotRayJobList.Items); diff != "" {
				t.Errorf("Unexpected ray jobs (-want/+got)\n%s", diff)
			}
		})
	}
}
