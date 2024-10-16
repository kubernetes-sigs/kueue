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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestLocalQueueFilter(t *testing.T) {
	testCases := map[string]struct {
		options *LocalQueueOptions
		in      *v1beta1.LocalQueueList
		out     *v1beta1.LocalQueueList
	}{
		"shouldn't filter": {
			options: &LocalQueueOptions{},
			in: &v1beta1.LocalQueueList{
				Items: []v1beta1.LocalQueue{
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
					},
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq2"},
					},
				},
			},
			out: &v1beta1.LocalQueueList{
				Items: []v1beta1.LocalQueue{
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
					},
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq2"},
					},
				},
			},
		},
		"should filter by cluster queue": {
			options: &LocalQueueOptions{
				ClusterQueueFilter: "cq1",
			},
			in: &v1beta1.LocalQueueList{
				Items: []v1beta1.LocalQueue{
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
					},
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq2"},
					},
				},
			},
			out: &v1beta1.LocalQueueList{
				Items: []v1beta1.LocalQueue{
					{
						Spec: v1beta1.LocalQueueSpec{ClusterQueue: "cq1"},
					},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tc.options.filterList(tc.in)
			if diff := cmp.Diff(tc.out, tc.in); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestLocalQueueCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print local queue list with namespace filter": {
			ns: "ns1",
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", "ns2").
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            1                   1                    60m
`,
		},
		"should print local queue list with clusterqueue filter": {
			args: []string{"--clusterqueue", "cq1"},
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            1                   1                    60m
`,
		},
		"should print local queue list with label selector filter": {
			args: []string{"--selector", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Label("key", "value1").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            1                   1                    60m
`,
		},
		"should print local queue list with label selector filter (short flag)": {
			args: []string{"-l", "foo=bar"},
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Label("foo", "bar").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
lq1    cq1            1                   1                    60m
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

			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewLocalQueueCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
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
