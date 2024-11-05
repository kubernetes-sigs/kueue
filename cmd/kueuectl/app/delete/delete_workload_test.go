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
	bactchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	kubetesting "k8s.io/client-go/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	testingworkload "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestWorkloadCmd(t *testing.T) {
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

	testCases := map[string]struct {
		ns            string
		args          []string
		input         string
		workloads     []runtime.Object
		jobs          []runtime.Object
		gvk           schema.GroupVersionKind
		wantWorkloads []v1beta1.Workload
		wantJobList   runtime.Object
		ignoreOut     bool
		wantOut       string
		wantOutErr    string
		wantErr       string
	}{
		"no arguments": {
			args:    []string{},
			wantErr: "requires at least 1 arg(s), only received 0",
		},
		"shouldn't delete a workload and its corresponding job without confirmation": {
			args: []string{"wl1"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: `This operation will also delete:
  - jobs.batch/j1 associated with the default/wl1 workload
Do you want to proceed (y/n)? Deletion is canceled
`,
		},
		"should delete just a workload without confirmation because there is no associated job requiring it": {
			args: []string{"wl2"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault},
					},
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: "workload.kueue.x-k8s.io/wl2 deleted\n",
		},
		"shouldn't delete a workload because it is not found": {
			args:       []string{"wl2"},
			wantOutErr: "workloads.kueue.x-k8s.io \"wl2\" not found\n",
		},
		"should delete jobs corresponding to the workload with confirmation": {
			args:  []string{"wl1"},
			input: "y\n",
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				*testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: `This operation will also delete:
  - jobs.batch/j1 associated with the default/wl1 workload
Do you want to proceed (y/n)? jobs.batch/j1 deleted
`,
		},
		"should delete jobs corresponding to the workload with yes flag": {
			args: []string{"wl1", "--yes"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				*testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: "jobs.batch/j1 deleted\n",
		},
		"should delete all jobs corresponding to the workloads and any workloads without corresponding jobs in default namespace": {
			args: []string{"--all", "--yes"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
				testingworkload.MakeWorkload("wl3", "test").Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				*testingworkload.MakeWorkload("wl3", "test").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			// We do not know in which order the result will be displayed.
			ignoreOut: true,
		},
		"should delete all jobs corresponding to the workloads and any workloads without corresponding jobs in all namespaces": {
			args: []string{"--all", "-y", "-A"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
				testingworkload.MakeWorkload("wl2", metav1.NamespaceDefault).Obj(),
				testingworkload.MakeWorkload("wl3", "test").Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j2", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			// We do not know in which order the result will be displayed.
			ignoreOut: true,
		},
		"shouldn't delete the jobs corresponding to the workloads jobs when using client dry-run flag": {
			args: []string{"wl1", "--dry-run", "client"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: "jobs.batch/j1 deleted (client dry run)\n",
		},
		"shouldn't delete the jobs corresponding to the workloads jobs when using server dry-run flag": {
			args: []string{"wl1", "--dry-run", "server"},
			workloads: []runtime.Object{
				testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			jobs: []runtime.Object{
				&bactchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault}},
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantWorkloads: []v1beta1.Workload{
				*testingworkload.MakeWorkload("wl1", metav1.NamespaceDefault).OwnerReference(jobGVK, "j1", "").Obj(),
			},
			wantJobList: &bactchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []bactchv1.Job{
					{
						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: "j1", Namespace: metav1.NamespaceDefault},
					},
				},
			},
			wantOut: "jobs.batch/j1 deleted (server dry run)\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ns := metav1.NamespaceDefault
			if tc.ns != "" {
				ns = tc.ns
			}

			streams, in, out, outErr := genericiooptions.NewTestIOStreams()
			clientset := fake.NewSimpleClientset(tc.workloads...)
			clientset.PrependReactor("delete", "workloads", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				if slices.Contains(action.(kubetesting.DeleteAction).GetDeleteOptions().DryRun, metav1.DryRunAll) {
					handled = true
				}
				return handled, ret, err
			})

			dynamicClient := dynamicfake.NewSimpleDynamicClient(k8sscheme.Scheme, tc.jobs...)
			restMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
			var (
				mapping *meta.RESTMapping
				err     error
			)
			if !tc.gvk.Empty() {
				restMapper.Add(tc.gvk, meta.RESTScopeNamespace)
				mapping, err = restMapper.RESTMapping(tc.gvk.GroupKind(), tc.gvk.Version)
				if err != nil {
					t.Fatal(err)
				}
				dynamicClient.PrependReactor("delete", mapping.Resource.Resource, func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
					// SimpleDynamicClient still don't have DryRun option on delete Reactor.
					if slices.Contains(tc.args, "--dry-run") {
						handled = true
					}
					return handled, ret, err
				})
			}

			tcg := cmdtesting.NewTestClientGetter().
				WithNamespace(ns).
				WithKueueClientset(clientset).
				WithDynamicClient(dynamicClient).
				WithRESTMapper(restMapper)

			cmd := NewWorkloadCmd(tcg, streams)
			cmd.SetIn(in)
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(tc.args)

			in.WriteString(tc.input)

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

			if !tc.ignoreOut {
				gotOut := out.String()
				if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
					t.Errorf("Unexpected output (-want/+got)\n%s", diff)
				}
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected error output (-want/+got)\n%s", diff)
			}

			gotWorkloadList, err := clientset.KueueV1beta1().Workloads(tc.ns).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloadList.Items); diff != "" {
				t.Errorf("Unexpected error output (-want/+got)\n%s", diff)
			}

			if tc.wantJobList == nil {
				return
			}

			unstructured, err := dynamicClient.Resource(mapping.Resource).Namespace(metav1.NamespaceDefault).
				List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Error(err)
				return
			}

			gotList := tc.wantJobList.DeepCopyObject()

			err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), gotList)
			if err != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantJobList, gotList); diff != "" {
				t.Errorf("Unexpected list for %s (-want/+got)\n%s", tc.gvk.String(), diff)
			}
		})
	}
}
