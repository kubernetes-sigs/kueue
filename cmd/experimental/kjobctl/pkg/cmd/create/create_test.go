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
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuefake "sigs.k8s.io/kueue/client-go/clientset/versioned/fake"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	cmdutil "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestCreateOptions_Complete(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		args        []string
		options     *CreateOptions
		wantOptions *CreateOptions
		wantErr     string
	}{
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

			cmd := NewCreateCmd(tcg, streams, clocktesting.NewFakeClock(testStartTime))
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(tc.args)

			gotErr := tc.options.Complete(tcg, cmd.Commands()[0], nil)

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

type createCmdTestCase struct {
	beforeTest     func(t *testing.T, tc *createCmdTestCase)
	tempFile       string
	ns             string
	args           func(tc *createCmdTestCase) []string
	kjobctlObjs    []runtime.Object
	kueueObjs      []runtime.Object
	gvks           []schema.GroupVersionKind
	wantLists      []runtime.Object
	cmpopts        []cmp.Option
	wantOut        string
	wantOutPattern string
	wantOutErr     string
	wantErr        string
}

func beforeSlurmTest(t *testing.T, tc *createCmdTestCase) {
	file, err := os.CreateTemp("", "slurm")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Cleanup(func() {
		if err := os.Remove(tc.tempFile); err != nil {
			t.Fatal(err)
		}
	})

	if _, err := file.WriteString("#!/bin/bash\nsleep 300'"); err != nil {
		t.Fatal(err)
	}

	tc.tempFile = file.Name()
}

func TestCreateCmd(t *testing.T) {
	testStartTime := time.Now()
	userID := os.Getenv(constants.SystemEnvVarNameUser)

	testCases := map[string]createCmdTestCase{
		"should create job": {
			args: func(tc *createCmdTestCase) []string { return []string{"job", "--profile", "profile"} },
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create rayjob": {
			args: func(tc *createCmdTestCase) []string { return []string{"rayjob", "--profile", "profile"} },
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create raycluster": {
			args: func(tc *createCmdTestCase) []string { return []string{"raycluster", "--profile", "profile"} },
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayClusterTemplate("ray-cluster-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayClusterMode, "ray-cluster-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayCluster"}},
			wantLists: []runtime.Object{
				&rayv1.RayClusterList{
					TypeMeta: metav1.TypeMeta{Kind: "RayClusterList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayCluster{
						*wrappers.MakeRayCluster("", metav1.NamespaceDefault).
							GenerateName("profile-raycluster-").
							Profile("profile").
							Mode(v1alpha1.RayClusterMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "raycluster.ray.io/<unknown> created\n",
		},
		"should create job with short profile flag": {
			args: func(tc *createCmdTestCase) []string { return []string{"job", "-p", "profile"} },
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with localqueue replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--localqueue", "lq1"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.LocalQueue{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "lq1",
					},
				},
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							LocalQueue("lq1").
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with localqueue and skip local queue validation": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--localqueue", "lq1", "--skip-localqueue-validation"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							LocalQueue("lq1").
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with parallelism replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--parallelism", "5"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Parallelism(5).
							Completions(1).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with --priority flag": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--priority", "sample-priority"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sample-priority",
					},
				},
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with --priority flag and skip workload priority class validation": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--skip-priority-validation", "--priority", "sample-priority"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with completions replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--completions", "5"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Parallelism(1).
					Completions(1).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Parallelism(1).
							Completions(5).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with command replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--cmd", "sleep 15s"}
			},
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
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Parallelism(1).
							Completions(1).
							WithContainer(*wrappers.MakeContainer("c1", "sleep").Command("sleep", "15s").Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "sleep").Obj()).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create job with request replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--request", "cpu=100m,ram=3Gi"}
			},
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
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
							Mode(v1alpha1.JobMode).
							Parallelism(1).
							Completions(1).
							WithContainer(
								*wrappers.MakeContainer("c1", "sleep").
									WithRequest("cpu", resource.MustParse("100m")).
									WithRequest("ram", resource.MustParse("3Gi")).
									Obj(),
							).
							WithContainer(*wrappers.MakeContainer("c2", "sleep").Obj()).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created\n",
		},
		"should create ray job with replicas replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--replicas", "g1=5"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					WithRayClusterSpec(
						wrappers.MakeRayClusterSpec().
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").Obj()).
							Obj(),
					).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").Replicas(5).Obj()).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with cmd replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--cmd", "sleep   3s"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							Entrypoint("sleep 3s").
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with min-replicas replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--min-replicas", "g1=5"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					WithRayClusterSpec(
						wrappers.MakeRayClusterSpec().
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").Obj()).
							Obj(),
					).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").MinReplicas(5).Obj()).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with max-replicas replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--max-replicas", "g1=5"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					WithRayClusterSpec(
						wrappers.MakeRayClusterSpec().
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").Obj()).
							Obj(),
					).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").MaxReplicas(5).Obj()).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with raycluster replacement": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--raycluster", "rc1"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					WithRayClusterSpec(
						wrappers.MakeRayClusterSpec().
							WithWorkerGroupSpec(*wrappers.MakeWorkerGroupSpec("g1").Obj()).
							Obj(),
					).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							WithRayClusterLabelSelector("rc1").
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with --priority flag": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--priority", "sample-priority"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sample-priority",
					},
				},
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"should create ray job with --priority flag and skip workload priority class validation": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--skip-priority-validation", "--priority", "sample-priority"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayJobMode, "ray-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}},
			wantLists: []runtime.Object{
				&rayv1.RayJobList{
					TypeMeta: metav1.TypeMeta{Kind: "RayJobList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayJob{
						*wrappers.MakeRayJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-rayjob-").
							Profile("profile").
							Mode(v1alpha1.RayJobMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "rayjob.ray.io/<unknown> created\n",
		},
		"shouldn't create ray job with raycluster and localqueue replacements because mutually exclusive": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--raycluster", "rc1", "--localqueue", "lq1"}
			},
			wantErr: "if any flags in the group [raycluster localqueue] are set none of the others can be; [localqueue raycluster] were all set",
		},
		"shouldn't create ray job with raycluster and replicas replacements because mutually exclusive": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--raycluster", "rc1", "--replicas", "g1=5"}
			},
			wantErr: "if any flags in the group [raycluster replicas] are set none of the others can be; [raycluster replicas] were all set",
		},
		"shouldn't create ray job with raycluster and min-replicas replacements because mutually exclusive": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--raycluster", "rc1", "--min-replicas", "g1=5"}
			},
			wantErr: "if any flags in the group [raycluster min-replicas] are set none of the others can be; [min-replicas raycluster] were all set",
		},
		"shouldn't create ray job with raycluster and max-replicas replacements because mutually exclusive": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"rayjob", "--profile", "profile", "--raycluster", "rc1", "--max-replicas", "g1=5"}
			},
			wantErr: "if any flags in the group [raycluster max-replicas] are set none of the others can be; [max-replicas raycluster] were all set",
		},
		"should create raycluster with array ": {
			args: func(tc *createCmdTestCase) []string { return []string{"raycluster", "--profile", "profile"} },
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayClusterTemplate("ray-cluster-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayClusterMode, "ray-cluster-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayCluster"}},
			wantLists: []runtime.Object{
				&rayv1.RayClusterList{
					TypeMeta: metav1.TypeMeta{Kind: "RayClusterList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayCluster{
						*wrappers.MakeRayCluster("", metav1.NamespaceDefault).
							GenerateName("profile-raycluster-").
							Profile("profile").
							Mode(v1alpha1.RayClusterMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "raycluster.ray.io/<unknown> created\n",
		},
		"should create raycluster with --priority flag": {
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"raycluster",
					"--profile", "profile",
					"--priority", "sample-priority",
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayClusterTemplate("ray-cluster-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayClusterMode, "ray-cluster-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sample-priority",
					},
				},
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayCluster"}},
			wantLists: []runtime.Object{
				&rayv1.RayClusterList{
					TypeMeta: metav1.TypeMeta{Kind: "RayClusterList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayCluster{
						*wrappers.MakeRayCluster("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-raycluster-").
							Profile("profile").
							Mode(v1alpha1.RayClusterMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "raycluster.ray.io/<unknown> created\n",
		},
		"should create raycluster with --priority flag and skip workload priority class validation": {
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"raycluster",
					"--profile", "profile",
					"--skip-priority-validation",
					"--priority", "sample-priority",
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeRayClusterTemplate("ray-cluster-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.RayClusterMode, "ray-cluster-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayCluster"}},
			wantLists: []runtime.Object{
				&rayv1.RayClusterList{
					TypeMeta: metav1.TypeMeta{Kind: "RayClusterList", APIVersion: "ray.io/v1"},
					Items: []rayv1.RayCluster{
						*wrappers.MakeRayCluster("", metav1.NamespaceDefault).
							Priority("sample-priority").
							GenerateName("profile-raycluster-").
							Profile("profile").
							Mode(v1alpha1.RayClusterMode).
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "raycluster.ray.io/<unknown> created\n",
		},
		"shouldn't create slurm because slurm args must be specified": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", "--profile", "profile"}
			},
			wantErr: "requires at least 1 arg(s), only received 0",
		},
		"shouldn't create slurm because script must be specified on slurm args": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", "--profile", "profile", "./script.sh"}
			},
			wantErr: "unknown command \"./script.sh\" for \"create slurm\"",
		},
		"shouldn't create slurm because script must be specified": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", "--profile", "profile", "--", "--array", "0-5"}
			},
			wantErr: "must specify script",
		},
		"shouldn't create slurm because script only one script must be specified": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", "--profile", "profile", "--", "./script.sh", "./script.sh"}
			},
			wantErr: "must specify only one script",
		},
		"should create slurm": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", "--profile", "profile", "--", tc.tempFile}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("profile-slurm", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Subdomain("profile-slurm").
							WithInitContainer(*wrappers.MakeContainer("slurm-init-env", "registry.k8s.io/busybox:1.27.2").
								Command("sh", "/slurm/scripts/init-entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithEnvVar(corev1.EnvVar{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
								}}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								Obj()).
							WithVolume(corev1.Volume{
								Name: "slurm-scripts",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{Name: "profile-slurm"},
										Items: []corev1.KeyToPath{
											{Key: "init-entrypoint.sh", Path: "init-entrypoint.sh"},
											{Key: "entrypoint.sh", Path: "entrypoint.sh"},
											{Key: "script", Path: "script", Mode: ptr.To[int32](0755)},
										},
									},
								},
							}).
							WithVolume(corev1.Volume{
								Name: "slurm-env",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							WithEnvVar(corev1.EnvVar{Name: "JOB_CONTAINER_INDEX", Value: "0"}).
							Obj(),
					},
				},
				&corev1.ConfigMapList{
					TypeMeta: metav1.TypeMeta{Kind: "ConfigMapList", APIVersion: "v1"},
					Items: []corev1.ConfigMap{
						*wrappers.MakeConfigMap("profile-slurm", metav1.NamespaceDefault).
							WithOwnerReference(metav1.OwnerReference{
								Name:       "profile-slurm",
								APIVersion: "batch/v1",
								Kind:       "Job",
							}).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Data(map[string]string{
								"script": "#!/bin/bash\nsleep 300'",
								"init-entrypoint.sh": `#!/bin/sh

set -o errexit
set -o nounset
set -o pipefail
set -x

# External variables
# JOB_COMPLETION_INDEX - completion index of the job.
# POD_IP               - current pod IP

array_indexes="0"
container_indexes=$(echo "$array_indexes" | awk -F';' -v idx="$JOB_COMPLETION_INDEX" '{print $((idx + 1))}')

for i in $(seq 0 1)
do
  container_index=$(echo "$container_indexes" | awk -F',' -v idx="$i" '{print $((idx + 1))}')

	if [ -z "$container_index" ]; then
		break
	fi

	mkdir -p /slurm/env/$i


	cat << EOF > /slurm/env/$i/slurm.env
SLURM_ARRAY_JOB_ID=1
SLURM_ARRAY_TASK_COUNT=1
SLURM_ARRAY_TASK_MAX=0
SLURM_ARRAY_TASK_MIN=0
SLURM_TASKS_PER_NODE=1
SLURM_CPUS_PER_TASK=
SLURM_CPUS_ON_NODE=
SLURM_JOB_CPUS_PER_NODE=
SLURM_CPUS_PER_GPU=
SLURM_MEM_PER_CPU=
SLURM_MEM_PER_GPU=
SLURM_MEM_PER_NODE=
SLURM_GPUS=
SLURM_NTASKS=1
SLURM_NTASKS_PER_NODE=1
SLURM_NPROCS=1
SLURM_NNODES=1
SLURM_SUBMIT_DIR=/slurm/scripts
SLURM_SUBMIT_HOST=$HOSTNAME
SLURM_JOB_NODELIST=profile-slurm-0.profile-slurm
SLURM_JOB_FIRST_NODE=profile-slurm-0.profile-slurm
SLURM_JOB_ID=$(expr $JOB_COMPLETION_INDEX \* 1 + $i + 1)
SLURM_JOBID=$(expr $JOB_COMPLETION_INDEX \* 1 + $i + 1)
SLURM_ARRAY_TASK_ID=$container_index
SLURM_JOB_FIRST_NODE_IP=${SLURM_JOB_FIRST_NODE_IP:-""}
EOF

done
`,
								"entrypoint.sh": `#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_CONTAINER_INDEX 	- container index in the container template.

if [ ! -d "/slurm/env/$JOB_CONTAINER_INDEX" ]; then
	exit 0
fi

SBATCH_JOB_NAME=

export $(cat /slurm/env/$JOB_CONTAINER_INDEX/slurm.env | xargs)

/slurm/scripts/script
`,
							}).
							Obj(),
					},
				},
				&corev1.ServiceList{
					TypeMeta: metav1.TypeMeta{Kind: "ServiceList", APIVersion: "v1"},
					Items: []corev1.Service{
						*wrappers.MakeService("profile-slurm", metav1.NamespaceDefault).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							ClusterIP("None").
							Selector("job-name", "profile-slurm").
							WithOwnerReference(metav1.OwnerReference{
								Name:       "profile-slurm",
								APIVersion: "batch/v1",
								Kind:       "Job",
							}).
							Obj(),
					},
				},
			},
			cmpopts: []cmp.Option{
				cmpopts.AcyclicTransformer("RemoveGeneratedNameSuffixInString", func(val string) string {
					return regexp.MustCompile("(profile-slurm)(-.{5})").ReplaceAllString(val, "$1")
				}),
				cmpopts.AcyclicTransformer("RemoveGeneratedNameSuffixInMap", func(m map[string]string) map[string]string {
					for key, val := range m {
						m[key] = regexp.MustCompile("(profile-slurm)(-.{5})").ReplaceAllString(val, "$1")
					}
					return m
				}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with flags": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--localqueue", "lq1",
					"--init-image", "bash:latest",
					"--first-node-ip",
					"--first-node-ip-timeout", "29s",
					"--",
					"--array", "0-25",
					"--nodes", "2",
					"--ntasks", "3",
					"--input", "\\\\/home/%u/%x/stderr%%-%A-%a-%j-%N-%n-%t.out",
					"--output", "/home/%u/%x/stdout%%-%A-%a-%j-%N-%n-%t.out",
					"--error", "/home/%u/%x/stderr%%-%A-%a-%j-%N-%n-%t.out",
					"--job-name", "job-name",
					"--partition", "lq1",
					"--chdir", "/mydir",
					"--cpus-per-task", "2",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.LocalQueue{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
						Name:      "lq1",
					},
				},
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("profile-slurm", metav1.NamespaceDefault).
							Parallelism(2).
							Completions(9).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							LocalQueue("lq1").
							Subdomain("profile-slurm").
							WithInitContainer(*wrappers.MakeContainer("slurm-init-env", "bash:latest").
								Command("sh", "/slurm/scripts/init-entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithEnvVar(corev1.EnvVar{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
								}}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-0", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithRequest(corev1.ResourceCPU, resource.MustParse("2")).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithRequest(corev1.ResourceCPU, resource.MustParse("2")).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithRequest(corev1.ResourceCPU, resource.MustParse("2")).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-scripts", MountPath: "/slurm/scripts"}).
								WithVolumeMount(corev1.VolumeMount{Name: "slurm-env", MountPath: "/slurm/env"}).
								WithRequest(corev1.ResourceCPU, resource.MustParse("2")).
								Obj()).
							WithVolume(corev1.Volume{
								Name: "slurm-scripts",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{Name: "profile-slurm"},
										Items: []corev1.KeyToPath{
											{Key: "init-entrypoint.sh", Path: "init-entrypoint.sh"},
											{Key: "entrypoint.sh", Path: "entrypoint.sh"},
											{Key: "script", Path: "script", Mode: ptr.To[int32](0755)},
										},
									},
								},
							}).
							WithVolume(corev1.Volume{
								Name: "slurm-env",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							WithEnvVarIndexValue("JOB_CONTAINER_INDEX").
							Obj(),
					},
				},
				&corev1.ConfigMapList{
					TypeMeta: metav1.TypeMeta{Kind: "ConfigMapList", APIVersion: "v1"},
					Items: []corev1.ConfigMap{
						*wrappers.MakeConfigMap("profile-slurm", metav1.NamespaceDefault).
							WithOwnerReference(metav1.OwnerReference{
								Name:       "profile-slurm",
								APIVersion: "batch/v1",
								Kind:       "Job",
							}).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							LocalQueue("lq1").
							Data(map[string]string{
								"script": "#!/bin/bash\nsleep 300'",
								"init-entrypoint.sh": `#!/bin/sh

set -o errexit
set -o nounset
set -o pipefail
set -x

# External variables
# JOB_COMPLETION_INDEX - completion index of the job.
# POD_IP               - current pod IP

array_indexes="0,1,2;3,4,5;6,7,8;9,10,11;12,13,14;15,16,17;18,19,20;21,22,23;24,25"
container_indexes=$(echo "$array_indexes" | awk -F';' -v idx="$JOB_COMPLETION_INDEX" '{print $((idx + 1))}')

for i in $(seq 0 3)
do
  container_index=$(echo "$container_indexes" | awk -F',' -v idx="$i" '{print $((idx + 1))}')

	if [ -z "$container_index" ]; then
		break
	fi

	mkdir -p /slurm/env/$i


  if [[ "$JOB_COMPLETION_INDEX" -eq 0 ]]; then
    SLURM_JOB_FIRST_NODE_IP=${POD_IP}
  else
    timeout=29
    start_time=$(date +%s)
    while true; do
      ip=$(nslookup "profile-slurm-r8njg-0.profile-slurm-r8njg" | grep "Address 1" | awk 'NR==2 {print $3}') || true
      if [[ -n "$ip" ]]; then
        SLURM_JOB_FIRST_NODE_IP=$ip
        break
      else
        current_time=$(date +%s)
        elapsed_time=$((current_time - start_time))
        if [ "$elapsed_time" -ge "$timeout" ]; then
          echo "Timeout reached, IP address for the first node (profile-slurm-r8njg-0.profile-slurm-r8njg) not found."
          break
        fi
        echo "IP Address for the first node (profile-slurm-r8njg-0.profile-slurm-r8njg) not found, retrying..."
        sleep 1
      fi
    done
  fi

	cat << EOF > /slurm/env/$i/slurm.env
SLURM_ARRAY_JOB_ID=1
SLURM_ARRAY_TASK_COUNT=26
SLURM_ARRAY_TASK_MAX=25
SLURM_ARRAY_TASK_MIN=0
SLURM_TASKS_PER_NODE=3
SLURM_CPUS_PER_TASK=2
SLURM_CPUS_ON_NODE=8
SLURM_JOB_CPUS_PER_NODE=8
SLURM_CPUS_PER_GPU=
SLURM_MEM_PER_CPU=
SLURM_MEM_PER_GPU=
SLURM_MEM_PER_NODE=
SLURM_GPUS=
SLURM_NTASKS=3
SLURM_NTASKS_PER_NODE=3
SLURM_NPROCS=3
SLURM_NNODES=2
SLURM_SUBMIT_DIR=/slurm/scripts
SLURM_SUBMIT_HOST=$HOSTNAME
SLURM_JOB_NODELIST=profile-slurm-fpxnj-0.profile-slurm-fpxnj,profile-slurm-fpxnj-1.profile-slurm-fpxnj
SLURM_JOB_FIRST_NODE=profile-slurm-fpxnj-0.profile-slurm-fpxnj
SLURM_JOB_ID=$(expr $JOB_COMPLETION_INDEX \* 3 + $i + 1)
SLURM_JOBID=$(expr $JOB_COMPLETION_INDEX \* 3 + $i + 1)
SLURM_ARRAY_TASK_ID=$container_index
SLURM_JOB_FIRST_NODE_IP=${SLURM_JOB_FIRST_NODE_IP:-""}
EOF

done
`,
								"entrypoint.sh": `#!/usr/local/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External variables
# JOB_CONTAINER_INDEX 	- container index in the container template.

if [ ! -d "/slurm/env/$JOB_CONTAINER_INDEX" ]; then
	exit 0
fi

SBATCH_JOB_NAME=job-name

export $(cat /slurm/env/$JOB_CONTAINER_INDEX/slurm.env | xargs)cd /mydir

/slurm/scripts/script </home/%u/%x/stderr%%-%A-%a-%j-%N-%n-%t.out 1>/home/${USER_ID}/${SBATCH_JOB_NAME}/stdout%-${SLURM_ARRAY_JOB_ID}-${SLURM_ARRAY_TASK_ID}-${SLURM_JOB_ID}-${HOSTNAME}-${JOB_COMPLETION_INDEX}-${SLURM_ARRAY_TASK_ID}.out 2>/home/${USER_ID}/${SBATCH_JOB_NAME}/stderr%-${SLURM_ARRAY_JOB_ID}-${SLURM_ARRAY_TASK_ID}-${SLURM_JOB_ID}-${HOSTNAME}-${JOB_COMPLETION_INDEX}-${SLURM_ARRAY_TASK_ID}.out
`,
							}).
							Obj(),
					},
				},
				&corev1.ServiceList{
					TypeMeta: metav1.TypeMeta{Kind: "ServiceList", APIVersion: "v1"},
					Items: []corev1.Service{
						*wrappers.MakeService("profile-slurm", metav1.NamespaceDefault).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							LocalQueue("lq1").
							ClusterIP("None").
							Selector("job-name", "profile-slurm").
							WithOwnerReference(metav1.OwnerReference{
								Name:       "profile-slurm",
								APIVersion: "batch/v1",
								Kind:       "Job",
							}).
							Obj(),
					},
				},
			},
			cmpopts: []cmp.Option{
				cmpopts.AcyclicTransformer("RemoveGeneratedNameSuffixInString", func(val string) string {
					return regexp.MustCompile("(profile-slurm)(-.{5})").ReplaceAllString(val, "$1")
				}),
				cmpopts.AcyclicTransformer("RemoveGeneratedNameSuffixInMap", func(m map[string]string) map[string]string {
					for key, val := range m {
						m[key] = regexp.MustCompile("(profile-slurm)(-.{5})").ReplaceAllString(val, "$1")
					}
					return m
				}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with --ntasks flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--ntasks", "3",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Subdomain("profile-slurm").
							WithContainer(*wrappers.MakeContainer("c1-0", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should divide --mem exactly across containers": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--mem", "2G",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("1G"),
									},
								}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("1G"),
									},
								}).
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should handle non-exact --mem division across containers": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--mem", "1G",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("500M"),
									},
								}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("500M"),
									},
								}).
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with --mem-per-cpu flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--cpus-per-task", "2",
					"--mem-per-cpu", "500M",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("2"),
										corev1.ResourceMemory: resource.MustParse("1G"),
									},
								}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("2"),
										corev1.ResourceMemory: resource.MustParse("1G"),
									},
								}).
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"shouldn't create slurm with --mem-per-cpu flag because --cpus-per-task flag not specified": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--mem-per-cpu", "500M",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			wantErr: "no cpus-per-task specified",
		},
		"should create slurm with --priority flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--priority", "sample-priority",
					"--",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			kueueObjs: []runtime.Object{
				&kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sample-priority",
					},
				},
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with --mem-per-gpu flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--gpus-per-task", "volta:3,kepler:1",
					"--mem-per-gpu", "500M",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("2G"),
										"volta":               resource.MustParse("3"),
										"kepler":              resource.MustParse("1"),
									},
								}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								WithResources(corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("2G"),
										"volta":               resource.MustParse("3"),
										"kepler":              resource.MustParse("1"),
									},
								}).
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"shouldn't create slurm with --mem-per-gpu flag because --gpus-per-task flag not specified": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					"--mem-per-gpu", "500M",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					WithContainer(*wrappers.MakeContainer("c2", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			wantErr: "no gpus-per-task specified",
		},
		"should create slurm with --priority flag and skip workload priority class validation": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--priority", "sample-priority",
					"--skip-priority-validation",
					"--",
					tc.tempFile,
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Priority("sample-priority").
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Subdomain("profile-slurm").
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with --time flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					tc.tempFile,
					"--time", "1",
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							MaxExecTimeSecondsLabel("60").
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Subdomain("profile-slurm").
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with -t flag": {
			beforeTest: beforeSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm",
					"--profile", "profile",
					"--",
					tc.tempFile,
					"-t", "2-12:05:23",
				}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("slurm-job-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").Obj()).
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.SlurmMode, "slurm-job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{
				{Group: "batch", Version: "v1", Kind: "Job"},
				{Group: "", Version: "v1", Kind: "ConfigMap"},
				{Group: "", Version: "v1", Kind: "Service"},
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							MaxExecTimeSecondsLabel("216323").
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							Mode(v1alpha1.SlurmMode).
							Subdomain("profile-slurm").
							WithContainer(*wrappers.MakeContainer("c1", "bash:4.4").
								Command("bash", "/slurm/scripts/entrypoint.sh").
								Obj()).
							Obj(),
					},
				},
				&corev1.ConfigMapList{},
				&corev1.ServiceList{},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "InitContainers", "Subdomain"),
				cmpopts.IgnoreTypes([]corev1.EnvVar{}),
				cmpopts.IgnoreTypes([]corev1.Volume{}),
				cmpopts.IgnoreTypes([]corev1.VolumeMount{}),
				cmpopts.IgnoreTypes(corev1.ConfigMapList{}),
				cmpopts.IgnoreTypes(corev1.ServiceList{}),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"shouldn't create job with client dry run": {
			args: func(tc *createCmdTestCase) []string {
				return []string{"job", "--profile", "profile", "--dry-run", "client"}
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.JobMode, "job-template").Obj()).
					Obj(),
			},
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items:    []batchv1.Job{},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "job.batch/<unknown> created (client dry run)\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if tc.beforeTest != nil {
				tc.beforeTest(t, &tc)
			}

			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			scheme := runtime.NewScheme()
			utilruntime.Must(k8sscheme.AddToScheme(scheme))
			utilruntime.Must(rayv1.AddToScheme(scheme))

			clientset := kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...)
			dynamicClient := fake.NewSimpleDynamicClient(scheme)
			kueueClientset := kueuefake.NewSimpleClientset(tc.kueueObjs...)
			restMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})

			for _, gvk := range tc.gvks {
				restMapper.Add(gvk, meta.RESTScopeNamespace)
			}

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(clientset).
				WithDynamicClient(dynamicClient).
				WithKueueClientset(kueueClientset).
				WithRESTMapper(restMapper)
			if tc.ns != "" {
				tcg.WithNamespace(tc.ns)
			}

			cmd := NewCreateCmd(tcg, streams, clocktesting.NewFakeClock(testStartTime))
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(tc.args(&tc))

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
			if tc.wantOutPattern != "" {
				gotOut = strings.ReplaceAll(gotOut, "\n", "\\n")
				match, err := regexp.MatchString(tc.wantOutPattern, gotOut)
				if err != nil {
					t.Error(err)
					return
				}
				if !match {
					t.Errorf("Unexpected output. Not match pattern \"%s\":\n%s", tc.wantOutPattern, gotOut)
				}
			} else if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			for index, gvk := range tc.gvks {
				mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
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

				gotList := tc.wantLists[index].DeepCopyObject()

				err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured.UnstructuredContent(), gotList)
				if err != nil {
					t.Error(err)
					return
				}

				if diff := cmp.Diff(tc.wantLists[index], gotList, tc.cmpopts...); diff != "" {
					t.Errorf("Unexpected list for %s (-want/+got)\n%s", gvk.String(), diff)
				}
			}
		})
	}
}

func TestCreateOptionsRunInteractive(t *testing.T) {
	testStartTime := time.Now()
	userID := os.Getenv(constants.SystemEnvVarNameUser)

	testCases := map[string]struct {
		options        *CreateOptions
		k8sObjs        []runtime.Object
		kjobctlObjs    []runtime.Object
		createMutation func(pod *corev1.Pod)
		wantPodList    *corev1.PodList
		wantErr        string
	}{
		"success": {
			options: &CreateOptions{
				Namespace:   metav1.NamespaceDefault,
				ProfileName: "profile",
				ModeName:    v1alpha1.InteractiveMode,
				Attach:      &fakeRemoteAttach{},
				AttachFunc:  testAttachFunc,
			},
			k8sObjs: []runtime.Object{
				wrappers.MakePodTemplate("pod-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					Obj(),
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.InteractiveMode, "pod-template").Obj()).
					Obj(),
			},
			createMutation: func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodRunning
			},
			wantPodList: &corev1.PodList{
				Items: []corev1.Pod{
					*wrappers.MakePod("", metav1.NamespaceDefault).
						GenerateName("profile-interactive-").
						Profile("profile").
						Mode(v1alpha1.InteractiveMode).
						WithContainer(*wrappers.MakeContainer("c1", "sleep").
							TTY().
							Stdin().
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							Obj()).
						Phase(corev1.PodRunning).
						Obj(),
				},
			},
		},
		"success with remove interactive pod": {
			options: &CreateOptions{
				Namespace:            metav1.NamespaceDefault,
				ProfileName:          "profile",
				ModeName:             v1alpha1.InteractiveMode,
				RemoveInteractivePod: true,
				Attach:               &fakeRemoteAttach{},
				AttachFunc:           testAttachFunc,
			},
			k8sObjs: []runtime.Object{
				wrappers.MakePodTemplate("pod-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					Obj(),
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.InteractiveMode, "pod-template").Obj()).
					Obj(),
			},
			createMutation: func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodRunning
			},
			wantPodList: &corev1.PodList{},
		},
		"success with dry-run client": {
			options: &CreateOptions{
				Namespace:      metav1.NamespaceDefault,
				ProfileName:    "profile",
				ModeName:       v1alpha1.InteractiveMode,
				DryRunStrategy: cmdutil.DryRunClient,
				Attach:         &fakeRemoteAttach{},
				AttachFunc:     testAttachFunc,
			},
			k8sObjs: []runtime.Object{
				wrappers.MakePodTemplate("pod-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					Obj(),
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.InteractiveMode, "pod-template").Obj()).
					Obj(),
			},
			wantPodList: &corev1.PodList{},
		},
		"timeout waiting for pod": {
			options: &CreateOptions{
				Namespace:   metav1.NamespaceDefault,
				ProfileName: "profile",
				ModeName:    v1alpha1.InteractiveMode,
				Attach:      &fakeRemoteAttach{},
				AttachFunc:  testAttachFunc,
			},
			k8sObjs: []runtime.Object{
				wrappers.MakePodTemplate("pod-template", metav1.NamespaceDefault).
					WithContainer(*wrappers.MakeContainer("c1", "sleep").Obj()).
					Obj(),
			},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(*wrappers.MakeSupportedMode(v1alpha1.InteractiveMode, "pod-template").Obj()).
					Obj(),
			},
			wantPodList: &corev1.PodList{
				Items: []corev1.Pod{
					*wrappers.MakePod("", metav1.NamespaceDefault).
						GenerateName("profile-interactive-").
						Profile("profile").
						Mode(v1alpha1.InteractiveMode).
						WithContainer(*wrappers.MakeContainer("c1", "sleep").
							TTY().
							Stdin().
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
							WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{
								Name:  constants.EnvVarTaskID,
								Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
							}).
							WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
							WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
							Obj()).
						Obj(),
				},
			},
			wantErr: "context deadline exceeded",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()
			tc.options.IOStreams = streams
			tc.options.Out = out
			tc.options.ErrOut = outErr
			tc.options.PrintFlags = genericclioptions.NewPrintFlags("created").WithTypeSetter(k8sscheme.Scheme)
			printer, err := tc.options.PrintFlags.ToPrinter()
			if err != nil {
				t.Fatal(err)
			}
			tc.options.PrintObj = printer.PrintObj

			k8sClientset := k8sfake.NewSimpleClientset(tc.k8sObjs...)
			kjobctlClientset := kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...)
			dynamicClient := fake.NewSimpleDynamicClient(k8sscheme.Scheme)
			restMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
			restMapper.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}, meta.RESTScopeNamespace)

			dynamicClient.PrependReactor("create", "pods", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
				createAction := action.(kubetesting.CreateAction)

				unstructuredObj := createAction.GetObject().(*unstructured.Unstructured)
				unstructuredObj.SetName(unstructuredObj.GetGenerateName() + utilrand.String(5))

				pod := &corev1.Pod{}

				if err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.UnstructuredContent(), pod); err != nil {
					return true, nil, err
				}

				if tc.createMutation != nil {
					tc.createMutation(pod)
				}

				_, err = k8sClientset.CoreV1().Pods(pod.GetNamespace()).Create(context.Background(), pod, metav1.CreateOptions{})
				if err != nil {
					return true, nil, err
				}

				return true, unstructuredObj, err
			})

			tcg := cmdtesting.NewTestClientGetter().
				WithK8sClientset(k8sClientset).
				WithKjobctlClientset(kjobctlClientset).
				WithDynamicClient(dynamicClient).
				WithRESTMapper(restMapper)

			gotErr := tc.options.Run(context.Background(), tcg, testStartTime)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotPodList, err := k8sClientset.CoreV1().Pods(metav1.NamespaceDefault).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}

			defaultCmpOpts := []cmp.Option{cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name")}
			if diff := cmp.Diff(tc.wantPodList, gotPodList, defaultCmpOpts...); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

type fakeRemoteAttach struct {
	url *url.URL
	err error
}

func (f *fakeRemoteAttach) Attach(url *url.URL, _ *restclient.Config, _ io.Reader, _, _ io.Writer, _ bool, _ remotecommand.TerminalSizeQueue) error {
	f.url = url
	return f.err
}

func testAttachFunc(o *CreateOptions, _ *corev1.Container, sizeQueue remotecommand.TerminalSizeQueue, _ *corev1.Pod) func() error {
	return func() error {
		u, err := url.Parse("http://kjobctl.test")
		if err != nil {
			return err
		}

		return o.Attach.Attach(u, nil, nil, nil, nil, o.TTY, sizeQueue)
	}
}
