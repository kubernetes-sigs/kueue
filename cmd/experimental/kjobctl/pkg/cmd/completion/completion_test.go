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
	rayfake "github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/fake"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestJobNameCompletionFunc(t *testing.T) {
	args := []string{"job1"}
	objs := []runtime.Object{
		wrappers.MakeJob("job1", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeJob("job2", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeJob("job3", "test").Profile("p1").Obj(),
		wrappers.MakeJob("job4", metav1.NamespaceDefault).Obj(),
	}

	wantNames := []string{"job2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithK8sClientset(k8sfake.NewSimpleClientset(objs...))

	complFn := JobNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}

func TestRayJobNameCompletionFunc(t *testing.T) {
	args := []string{"ray-job1"}
	objs := []runtime.Object{
		wrappers.MakeRayJob("ray-job1", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeRayJob("ray-job2", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeRayJob("ray-job3", "test").Profile("p1").Obj(),
		wrappers.MakeRayJob("ray-job4", metav1.NamespaceDefault).Obj(),
	}

	wantNames := []string{"ray-job2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithRayClientset(rayfake.NewSimpleClientset(objs...))

	complFn := RayJobNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}

func TestRayClusterNameCompletionFunc(t *testing.T) {
	args := []string{"ray-cluster1"}
	objs := []runtime.Object{
		wrappers.MakeRayCluster("ray-cluster1", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeRayCluster("ray-cluster2", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeRayCluster("ray-cluster3", "test").Profile("p1").Obj(),
		wrappers.MakeRayCluster("ray-cluster4", metav1.NamespaceDefault).Obj(),
	}

	wantNames := []string{"ray-cluster2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithRayClientset(rayfake.NewSimpleClientset(objs...))

	complFn := RayClusterNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}
