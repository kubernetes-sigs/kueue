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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuefake "sigs.k8s.io/kueue/client-go/clientset/versioned/fake"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestNamespaceNameFunc(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns2"}},
	}

	wantNames := []string{"ns1", "ns2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithK8sClientset(k8sfake.NewSimpleClientset(objs...))

	complFn := NamespaceNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, []string{}, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}

func TestApplicationProfileNameFunc(t *testing.T) {
	args := []string{"ap1"}
	objs := []runtime.Object{
		wrappers.MakeApplicationProfile("ap1", metav1.NamespaceDefault).Obj(),
		wrappers.MakeApplicationProfile("ap2", metav1.NamespaceDefault).Obj(),
		wrappers.MakeApplicationProfile("ap3", "test").Obj(),
	}

	wantNames := []string{"ap2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithKjobctlClientset(fake.NewSimpleClientset(objs...))

	complFn := ApplicationProfileNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}

func TestLocalQueueNameFunc(t *testing.T) {
	args := []string{"lq1"}
	objs := []runtime.Object{
		&v1beta1.LocalQueue{ObjectMeta: metav1.ObjectMeta{Name: "lq1", Namespace: metav1.NamespaceDefault}},
		&v1beta1.LocalQueue{ObjectMeta: metav1.ObjectMeta{Name: "lq2", Namespace: metav1.NamespaceDefault}},
		&v1beta1.LocalQueue{ObjectMeta: metav1.ObjectMeta{Name: "lq3", Namespace: "test"}},
	}

	wantNames := []string{"lq2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithKueueClientset(kueuefake.NewSimpleClientset(objs...))

	complFn := LocalQueueNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}

func TestJobNameCompletionFunc(t *testing.T) {
	args := []string{"job1"}
	objs := []runtime.Object{
		wrappers.MakeJob("job1", metav1.NamespaceDefault).Profile("p1").Mode("jJb").Obj(),
		wrappers.MakeJob("job2", metav1.NamespaceDefault).Profile("p1").Mode("Job").Obj(),
		wrappers.MakeJob("job3", metav1.NamespaceDefault).Profile("p1").Mode("Slurm").Obj(),
		wrappers.MakeJob("job4", "test").Profile("p1").Mode("Job").Obj(),
		wrappers.MakeJob("job5", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakeJob("job6", metav1.NamespaceDefault).Mode("Job").Obj(),
	}
	mode := v1alpha1.JobMode

	wantNames := []string{"job2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithK8sClientset(k8sfake.NewSimpleClientset(objs...))

	complFn := JobNameFunc(tcg, mode)
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

func TestPodNameCompletionFunc(t *testing.T) {
	args := []string{"pod1"}
	objs := []runtime.Object{
		wrappers.MakePod("pod1", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakePod("pod2", metav1.NamespaceDefault).Profile("p1").Obj(),
		wrappers.MakePod("pod3", "test").Profile("p1").Obj(),
		wrappers.MakePod("pod4", metav1.NamespaceDefault).Obj(),
	}

	wantNames := []string{"pod2"}
	wantDirective := cobra.ShellCompDirectiveNoFileComp

	tcg := cmdtesting.NewTestClientGetter()
	tcg.WithK8sClientset(k8sfake.NewSimpleClientset(objs...))

	complFn := PodNameFunc(tcg)
	names, directive := complFn(&cobra.Command{}, args, "")
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("Unexpected names (-want/+got)\n%s", diff)
	}

	if diff := cmp.Diff(wantDirective, directive); diff != "" {
		t.Errorf("Unexpected directive (-want/+got)\n%s", diff)
	}
}
