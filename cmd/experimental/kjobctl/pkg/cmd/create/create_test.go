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

package create

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestCreateOptions_Complete(t *testing.T) {
	testCases := map[string]struct {
		args        []string
		options     *CreateOptions
		wantOptions *CreateOptions
		wantErr     string
	}{
		"invalid mode": {
			args:    []string{"invalid"},
			options: &CreateOptions{},
			wantErr: invalidApplicationProfileModeErr.Error(),
		},
		"invalid request": {
			args: []string{"job"},
			options: &CreateOptions{
				Namespace:            metav1.NamespaceDefault,
				ModeName:             v1alpha1.JobMode,
				UserSpecifiedRequest: map[string]string{"cpu": "invalid"},
			},
			wantOptions: &CreateOptions{},
			wantErr:     "quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()

			cmd := NewCreateCmd(tcg, streams)
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(tc.args)

			gotErr := tc.options.Complete(tcg, cmd, tc.args)

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
		})
	}
}

func TestCreateCmd(t *testing.T) {
	testCases := map[string]struct {
		ns          string
		args        []string
		kjobctlObjs []runtime.Object
		gvk         schema.GroupVersionKind
		wantList    runtime.Object
		wantOut     string
		wantOutErr  string
		wantErr     string
	}{
		"shouldn't create job without mode": {
			args:    []string{},
			wantErr: `accepts 1 arg(s), received 0`,
		},
		"shouldn't create job with invalid mode": {
			args:    []string{"invalid"},
			wantErr: `invalid argument "invalid" for "create"`,
		},
		"should create job": {
			args: []string{"job", "--profile", "profile"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with short profile flag": {
			args: []string{"job", "-p", "profile"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with localqueue replacement": {
			args: []string{"job", "--profile", "profile", "--localqueue", "lq1"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						LocalQueue("lq1").
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with parallelism replacement": {
			args: []string{"job", "--profile", "profile", "--parallelism", "5"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Parallelism(5).
						Completions(1).
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with completions replacement": {
			args: []string{"job", "--profile", "profile", "--completions", "5"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Parallelism(1).
						Completions(5).
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with command replacement": {
			args: []string{"job", "--profile", "profile", "--cmd", "sleep 15s"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "sleep").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Parallelism(1).
						Completions(1).
						WithContainer(*wrappers.MakeContainer("c1", "sleep").Command("sleep", "15s").Obj()).
						WithContainer(*wrappers.MakeContainer("c2", "sleep").Command("sleep", "15s").Obj()).
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with request replacement": {
			args: []string{"job", "--profile", "profile", "--request", "cpu=100m,ram=3Gi"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "sleep").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items: []batchv1.Job{
					*wrappers.MakeJob("", metav1.NamespaceDefault).
						GenerateName("profile-").
						Profile("profile").
						Parallelism(1).
						Completions(1).
						WithContainer(
							*wrappers.MakeContainer("c1", "sleep").
								WithRequest("cpu", resource.MustParse("100m")).
								WithRequest("ram", resource.MustParse("3Gi")).
								Obj(),
						).
						WithContainer(
							*wrappers.MakeContainer("c2", "sleep").
								WithRequest("cpu", resource.MustParse("100m")).
								WithRequest("ram", resource.MustParse("3Gi")).
								Obj(),
						).
						Obj(),
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"shouldn't create job with client dry run": {
			args: []string{"job", "--profile", "profile", "--dry-run", "client"},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvk: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			wantList: &batchv1.JobList{
				TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
				Items:    []batchv1.Job{},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created (client dry run)\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))

			clientset := kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...)
			dynamicClient := fake.NewSimpleDynamicClient(scheme)
			restMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})

			restMapper.Add(tc.gvk, meta.RESTScopeNamespace)

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(clientset).
				WithDynamicClient(dynamicClient).
				WithRESTMapper(restMapper)
			if tc.ns != "" {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewCreateCmd(tcg, streams)
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
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			mapping, err := restMapper.RESTMapping(tc.gvk.GroupKind(), tc.gvk.Version)
			if err != nil {
				t.Error(err)
				return
			}

			unstructured, err := dynamicClient.Resource(mapping.Resource).Namespace(metav1.NamespaceDefault).
				List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Error(err)
				return
			}

			gotJobList := tc.wantList.DeepCopyObject()

			err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), gotJobList)
			if err != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantList, gotJobList); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}
