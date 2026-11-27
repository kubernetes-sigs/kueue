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

package stop

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestStopCmd(t *testing.T) {
	testCases := map[string]struct {
		ns             string
		objs           []runtime.Object
		args           []string
		wantOut        string
		wantOutErr     string
		wantErr        error
		wantStopPolicy *kueue.StopPolicy
		wantActive     *bool
	}{
		"stop clusterqueue (HoldAndDrain by default)": {
			objs: []runtime.Object{
				utiltestingapi.MakeClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"clusterqueue", "cq1"},
			wantOut:        "clusterqueue.kueue.x-k8s.io/cq1 stopped\n",
			wantStopPolicy: new(kueue.HoldAndDrain),
		},
		"stop clusterqueue with short alias": {
			objs: []runtime.Object{
				utiltestingapi.MakeClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"cq", "cq1"},
			wantOut:        "clusterqueue.kueue.x-k8s.io/cq1 stopped\n",
			wantStopPolicy: new(kueue.HoldAndDrain),
		},
		"stop clusterqueue with keep-already-running (Hold)": {
			objs: []runtime.Object{
				utiltestingapi.MakeClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"clusterqueue", "cq1", "--keep-already-running"},
			wantOut:        "clusterqueue.kueue.x-k8s.io/cq1 stopped\n",
			wantStopPolicy: new(kueue.Hold),
		},
		"stop clusterqueue with client dry run": {
			objs: []runtime.Object{
				utiltestingapi.MakeClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"clusterqueue", "cq1", "--dry-run=client"},
			wantOut:        "clusterqueue.kueue.x-k8s.io/cq1 stopped (client dry run)\n",
			wantStopPolicy: new(kueue.None),
		},
		"stop clusterqueue with server dry run": {
			objs: []runtime.Object{
				utiltestingapi.MakeClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:    []string{"clusterqueue", "cq1", "--dry-run=server"},
			wantOut: "clusterqueue.kueue.x-k8s.io/cq1 stopped (server dry run)\n",
		},
		"stop clusterqueue not found": {
			args:       []string{"clusterqueue", "nonexistent"},
			wantOutErr: "Error: clusterqueues.kueue.x-k8s.io \"nonexistent\" not found\n",
			wantErr:    cmpopts.AnyError,
		},
		"stop localqueue (HoldAndDrain by default)": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"localqueue", "lq1"},
			wantOut:        "localqueue.kueue.x-k8s.io/lq1 stopped\n",
			wantStopPolicy: new(kueue.HoldAndDrain),
		},
		"stop localqueue with short alias": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"lq", "lq1"},
			wantOut:        "localqueue.kueue.x-k8s.io/lq1 stopped\n",
			wantStopPolicy: new(kueue.HoldAndDrain),
		},
		"stop localqueue with keep-already-running (Hold)": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"localqueue", "lq1", "--keep-already-running"},
			wantOut:        "localqueue.kueue.x-k8s.io/lq1 stopped\n",
			wantStopPolicy: new(kueue.Hold),
		},
		"stop localqueue with client dry run": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					StopPolicy(kueue.None).
					Obj(),
			},
			args:           []string{"localqueue", "lq1", "--dry-run=client"},
			wantOut:        "localqueue.kueue.x-k8s.io/lq1 stopped (client dry run)\n",
			wantStopPolicy: new(kueue.None),
		},
		"stop localqueue not found": {
			ns:         "ns1",
			args:       []string{"localqueue", "nonexistent"},
			wantOutErr: "Error: localqueues.kueue.x-k8s.io \"nonexistent\" not found\n",
			wantErr:    cmpopts.AnyError,
		},
		"stop workload": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeWorkload("wl1", "ns1").
					Active(true).
					Obj(),
			},
			args:       []string{"workload", "wl1"},
			wantOut:    "workload.kueue.x-k8s.io/wl1 stopped\n",
			wantActive: new(false),
		},
		"stop workload with short alias": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeWorkload("wl1", "ns1").
					Active(true).
					Obj(),
			},
			args:       []string{"kwl", "wl1"},
			wantOut:    "workload.kueue.x-k8s.io/wl1 stopped\n",
			wantActive: new(false),
		},
		"stop workload with client dry run": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltestingapi.MakeWorkload("wl1", "ns1").
					Active(true).
					Obj(),
			},
			args:       []string{"workload", "wl1", "--dry-run=client"},
			wantOut:    "workload.kueue.x-k8s.io/wl1 stopped (client dry run)\n",
			wantActive: new(true),
		},
		"stop workload not found": {
			ns:         "ns1",
			args:       []string{"workload", "nonexistent"},
			wantOutErr: "Error: workloads.kueue.x-k8s.io \"nonexistent\" not found\n",
			wantErr:    cmpopts.AnyError,
		},
		"reject unexpected positional argument for clusterqueue": {
			args:       []string{"clusterqueue", "cq1", "extra"},
			wantOutErr: "Error: accepts 1 arg(s), received 2\n",
			wantErr:    cmpopts.AnyError,
		},
		"reject unexpected positional argument for localqueue": {
			args:       []string{"localqueue", "lq1", "extra"},
			wantOutErr: "Error: accepts 1 arg(s), received 2\n",
			wantErr:    cmpopts.AnyError,
		},
		"reject unexpected positional argument for workload": {
			args:       []string{"workload", "wl1", "extra"},
			wantOutErr: "Error: accepts 1 arg(s), received 2\n",
			wantErr:    cmpopts.AnyError,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			clientset := fake.NewSimpleClientset(tc.objs...)
			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(clientset)
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewStopCmd(tcg, streams)
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SilenceUsage = true
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
				t.Errorf("Unexpected error output (-want/+got)\n%s", diff)
			}

			if tc.wantStopPolicy != nil {
				if len(tc.args) > 1 && (tc.args[0] == "clusterqueue" || tc.args[0] == "cq") {
					cq, err := clientset.KueueV1beta2().ClusterQueues().Get(t.Context(), tc.args[1], metav1.GetOptions{})
					if err == nil {
						if diff := cmp.Diff(tc.wantStopPolicy, cq.Spec.StopPolicy); diff != "" {
							t.Errorf("Unexpected ClusterQueue StopPolicy (-want/+got)\n%s", diff)
						}
					}
				}
				if len(tc.args) > 1 && (tc.args[0] == "localqueue" || tc.args[0] == "lq") {
					lq, err := clientset.KueueV1beta2().LocalQueues(tc.ns).Get(t.Context(), tc.args[1], metav1.GetOptions{})
					if err == nil {
						if diff := cmp.Diff(tc.wantStopPolicy, lq.Spec.StopPolicy); diff != "" {
							t.Errorf("Unexpected LocalQueue StopPolicy (-want/+got)\n%s", diff)
						}
					}
				}
			}

			if tc.wantActive != nil {
				if len(tc.args) > 1 && (tc.args[0] == "workload" || tc.args[0] == "kwl") {
					wl, err := clientset.KueueV1beta2().Workloads(tc.ns).Get(t.Context(), tc.args[1], metav1.GetOptions{})
					if err == nil {
						if diff := cmp.Diff(tc.wantActive, wl.Spec.Active); diff != "" {
							t.Errorf("Unexpected Workload Active (-want/+got)\n%s", diff)
						}
					}
				}
			}
		})
	}
}
