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

package completion

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestWorkloadNameCompletionFunc(t *testing.T) {
	testCases := map[string]struct {
		ns            string
		status        *bool
		toComplete    string
		objs          []runtime.Object
		args          []string
		wantNames     []string
		wantDirective cobra.ShellCompDirective
	}{
		"should return workload names": {
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).Active(true).Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).Active(false).Obj(),
			},
			wantNames:     []string{"wl1", "wl2"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return active workload names": {
			status: ptr.To(true),
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).Active(true).Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).Active(false).Obj(),
			},
			wantNames:     []string{"wl1"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return inactive workload names": {
			status: ptr.To(false),
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).Active(true).Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).Active(false).Obj(),
			},
			wantNames:     []string{"wl2"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should filter workload names": {
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", metav1.NamespaceDefault).Active(true).Obj(),
				utiltesting.MakeWorkload("wl2", metav1.NamespaceDefault).Active(false).Obj(),
			},
			args:          []string{"wl2"},
			wantNames:     []string{"wl1"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			complFn := WorkloadNameFunc(tcg, tc.status)
			names, directive := complFn(&cobra.Command{}, tc.args, tc.toComplete)
			if diff := cmp.Diff(tc.wantNames, names); diff != "" {
				t.Errorf("Unexpected names (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantDirective, directive); diff != "" {
				t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestClusterQueueNameCompletionFunc(t *testing.T) {
	testCases := map[string]struct {
		ns            string
		status        *bool
		toComplete    string
		objs          []runtime.Object
		args          []string
		wantNames     []string
		wantDirective cobra.ShellCompDirective
	}{
		"should return active cluster queue names": {
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeClusterQueue("cq2").StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeClusterQueue("cq3").StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"cq1", "cq2", "cq3"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return cluster queue names": {
			status: ptr.To(true),
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeClusterQueue("cq2").StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeClusterQueue("cq3").StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"cq1"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return inactive cluster queue names": {
			status: ptr.To(false),
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeClusterQueue("cq2").StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeClusterQueue("cq3").StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"cq2", "cq3"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"shouldn't return cluster queue names because only one argument can be passed": {
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeClusterQueue("cq2").StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeClusterQueue("cq3").StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			args:          []string{"cq2"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			complFn := ClusterQueueNameFunc(tcg, tc.status)
			names, directive := complFn(&cobra.Command{}, tc.args, tc.toComplete)
			if diff := cmp.Diff(tc.wantNames, names); diff != "" {
				t.Errorf("Unexpected names (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantDirective, directive); diff != "" {
				t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
			}
		})
	}
}

func TestLocalQueueNameCompletionFunc(t *testing.T) {
	testCases := map[string]struct {
		ns            string
		status        *bool
		toComplete    string
		objs          []runtime.Object
		args          []string
		wantNames     []string
		wantDirective cobra.ShellCompDirective
	}{
		"should return local queue names": {
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeLocalQueue("lq3", metav1.NamespaceDefault).StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"lq1", "lq2", "lq3"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return active local queue names": {
			status: ptr.To(true),
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeLocalQueue("lq3", metav1.NamespaceDefault).StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"lq1"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"should return inactive local queue names": {
			status: ptr.To(false),
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).StopPolicy(v1beta1.None).Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).StopPolicy(v1beta1.Hold).Obj(),
				utiltesting.MakeLocalQueue("lq3", metav1.NamespaceDefault).StopPolicy(v1beta1.HoldAndDrain).Obj(),
			},
			wantNames:     []string{"lq2", "lq3"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
		"shouldn't return local queue names because only one argument can be passed": {
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", metav1.NamespaceDefault).Obj(),
				utiltesting.MakeLocalQueue("lq2", metav1.NamespaceDefault).Obj(),
			},
			args:          []string{"lq1"},
			wantDirective: cobra.ShellCompDirectiveNoFileComp,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			complFn := LocalQueueNameFunc(tcg, tc.status)
			names, directive := complFn(&cobra.Command{}, tc.args, tc.toComplete)
			if diff := cmp.Diff(tc.wantNames, names); diff != "" {
				t.Errorf("Unexpected names (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantDirective, directive); diff != "" {
				t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
			}
		})
	}
}
