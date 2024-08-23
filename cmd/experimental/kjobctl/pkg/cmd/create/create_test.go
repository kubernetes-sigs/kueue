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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clocktesting "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
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
	beforeTest     func(tc *createCmdTestCase) error
	afterTest      func(tc *createCmdTestCase) error
	tempFile       string
	ns             string
	args           func(tc *createCmdTestCase) []string
	kjobctlObjs    []runtime.Object
	gvks           []schema.GroupVersionKind
	wantLists      []runtime.Object
	cmpopts        []cmp.Option
	wantOut        string
	wantOutPattern string
	wantOutErr     string
	wantErr        string
}

func beforeSlurmTest(tc *createCmdTestCase) error {
	file, err := os.CreateTemp("", "slurm")
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.WriteString("#!/bin/bash\nsleep 300'"); err != nil {
		return err
	}

	tc.tempFile = file.Name()

	return nil
}

func afterSlurmTest(tc *createCmdTestCase) error {
	return os.Remove(tc.tempFile)
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
			gvks: []schema.GroupVersionKind{{Group: "batch", Version: "v1", Kind: "Job"}},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							GenerateName("profile-job-").
							Profile("profile").
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
							Parallelism(5).
							Completions(1).
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
							WithRayClusterLabelSelector("rc1").
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
							Obj(),
					},
				},
			},
			// Fake dynamic client not generating name. That's why we have <unknown>.
			wantOut: "raycluster.ray.io/<unknown> created\n",
		},
		"should create slurm": {
			beforeTest: beforeSlurmTest,
			afterTest:  afterSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{"slurm", tc.tempFile, "--profile", "profile"}
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
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Completions(1).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							WithContainer(*wrappers.MakeContainer("c1-0", "bash:4.4").
								Command("bash", "/slurm/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{MountPath: "/slurm"}).
								Obj()).
							WithVolume(corev1.Volume{
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										Items: []corev1.KeyToPath{
											{Key: "entrypoint.sh", Path: "entrypoint.sh"},
											{Key: "script.sh", Path: "script.sh"},
										},
									},
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
						*wrappers.MakeConfigMap("", metav1.NamespaceDefault).
							Profile("profile").
							Data(map[string]string{
								"script.sh": "#!/bin/bash\nsleep 300'",
								"entrypoint.sh": `#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A ARRAY_INDEXES=(["0"]="0") 	# Requires bash v4+

CONTAINER_INDEXES=${ARRAY_INDEXES[${JOB_COMPLETION_INDEX}]}
CONTAINER_INDEXES=(${CONTAINER_INDEXES//,/ })

if [[ ! -v CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder

export SBATCH_INPUT= 		# Instruct Slurm to connect the batch script's standard input directly to the file name specified in the "filename pattern".
export SBATCH_OUTPUT=		# Instruct Slurm to connect the batch script's standard output directly to the file name specified in the "filename pattern".
export SBATCH_ERROR=		# Instruct Slurm to connect the batch script's standard error directly to the file name specified in the "filename pattern".

export SLURM_ARRAY_JOB_ID=1       		# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=1  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=0    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=1    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       		# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        		# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   		# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        		# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         	# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         	# Memory per GPU.
export SLURM_MEM_PER_NODE=        	# Memory per node. Same as --mem.
export SLURM_GPUS=                	# Number of GPUs requested (in total).
export SLURM_NTASKS=1              	# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=1  		# Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=1            		# Total number of nodes (actually pods) in the job’s resource allocation.
# export SLURM_SUBMIT_DIR=/slurm        	# The path of the job submission directory.
# export SLURM_SUBMIT_HOST=$HOSTNAME       	# The hostname of the node used for job submission.
export SBATCH_JOB_NAME=				# Specified job name.

# To be supported later
# export SLURM_JOB_NODELIST=        # Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later.
# export SLURM_NODELIST=            # Deprecated. Same as SLURM_JOB_NODELIST. To be supported later.
# export SLURM_NTASKS_PER_SOCKET    # Number of tasks requested per socket. To be supported later.
# export SLURM_NTASKS_PER_CORE      # Number of tasks requested per core. To be supported later.
# export SLURM_NTASKS_PER_GPU       # Number of tasks requested per GPU. To be supported later.

# Calculated variables in runtime
export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}]}												# Task ID.

# fill_file_name fills file name by pattern
# \\ - Do not process any of the replacement symbols.
# %% - The character "%".
# %A - Job array's master job allocation number (for now it is equivalent to SLURM_JOB_ID).
# %a - Job array ID (index) number (SLURM_ARRAY_TASK_ID).
# %j - jobid of the running job (SLURM_JOB_ID).
# %N - short hostname (pod name).
# %n - node(pod) identifier relative to current job - index from K8S index job.
# %t - task identifier (rank) relative to current job - It is array id position.
# %u - user name (from the client machine).
# %x - job name.
fill_file_name () {
  REPLACED="$1"

  if [[ "$REPLACED" == "\\"* ]]; then
      REPLACED="${REPLACED//\\/}"
      echo "${REPLACED}"
      return 0
  fi

  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%A)/\1\n\2/g;:a s/(^|[^\n])%A/\1$SLURM_ARRAY_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%a)/\1\n\2/g;:a s/(^|[^\n])%a/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%j)/\1\n\2/g;:a s/(^|[^\n])%j/\1$SLURM_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%N)/\1\n\2/g;:a s/(^|[^\n])%N/\1$HOSTNAME/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%n)/\1\n\2/g;:a s/(^|[^\n])%n/\1$JOB_COMPLETION_INDEX/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%t)/\1\n\2/g;:a s/(^|[^\n])%t/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%u)/\1\n\2/g;:a s/(^|[^\n])%u/\1$USER_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%x)/\1\n\2/g;:a s/(^|[^\n])%x/\1$SBATCH_JOB_NAME/;ta;s/\n//g")

  REPLACED="${REPLACED//%%/%}"

  echo "$REPLACED"
}

export SBATCH_INPUT=$(fill_file_name "$SBATCH_INPUT")
export SBATCH_OUTPUT=$(fill_file_name "$SBATCH_OUTPUT")
export SBATCH_ERROR=$(fill_file_name "$SBATCH_ERROR")

bash /slurm/script.sh
`,
							}).
							Obj(),
					},
				},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.Volume{}, "Name"),
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.VolumeMount{}, "Name"),
			},
			wantOutPattern: `job\.batch\/.+ created\\nconfigmap\/.+ created`,
		},
		"should create slurm with flags": {
			beforeTest: beforeSlurmTest,
			afterTest:  afterSlurmTest,
			args: func(tc *createCmdTestCase) []string {
				return []string{
					"slurm", tc.tempFile,
					"--profile", "profile",
					"--localqueue", "lq1",
					"--array", "0-25",
					"--nodes", "2",
					"--ntasks", "3",
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
			},
			wantLists: []runtime.Object{
				&batchv1.JobList{
					TypeMeta: metav1.TypeMeta{Kind: "JobList", APIVersion: "batch/v1"},
					Items: []batchv1.Job{
						*wrappers.MakeJob("", metav1.NamespaceDefault).
							Parallelism(2).
							Completions(9).
							CompletionMode(batchv1.IndexedCompletion).
							Profile("profile").
							LocalQueue("lq1").
							WithContainer(*wrappers.MakeContainer("c1-0", "bash:4.4").
								Command("bash", "/slurm/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{MountPath: "/slurm"}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-1", "bash:4.4").
								Command("bash", "/slurm/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{MountPath: "/slurm"}).
								Obj()).
							WithContainer(*wrappers.MakeContainer("c1-2", "bash:4.4").
								Command("bash", "/slurm/entrypoint.sh").
								WithVolumeMount(corev1.VolumeMount{MountPath: "/slurm"}).
								Obj()).
							WithVolume(corev1.Volume{
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										Items: []corev1.KeyToPath{
											{Key: "entrypoint.sh", Path: "entrypoint.sh"},
											{Key: "script.sh", Path: "script.sh"},
										},
									},
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
						*wrappers.MakeConfigMap("", metav1.NamespaceDefault).
							Profile("profile").
							LocalQueue("lq1").
							Data(map[string]string{
								"script.sh": "#!/bin/bash\nsleep 300'",
								"entrypoint.sh": `#!/usr/bin/bash

set -o errexit
set -o nounset
set -o pipefail

# External
# JOB_COMPLETION_INDEX  - completion index of the job.
# JOB_CONTAINER_INDEX   - container index in the container template.

# ["COMPLETION_INDEX"]=CONTAINER_INDEX1,CONTAINER_INDEX2
declare -A ARRAY_INDEXES=(["0"]="0,1,2" ["1"]="3,4,5" ["2"]="6,7,8" ["3"]="9,10,11" ["4"]="12,13,14" ["5"]="15,16,17" ["6"]="18,19,20" ["7"]="21,22,23" ["8"]="24,25") 	# Requires bash v4+

CONTAINER_INDEXES=${ARRAY_INDEXES[${JOB_COMPLETION_INDEX}]}
CONTAINER_INDEXES=(${CONTAINER_INDEXES//,/ })

if [[ ! -v CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}] ]];
then
	exit 0
fi

# Generated on the builder

export SBATCH_INPUT= 		# Instruct Slurm to connect the batch script's standard input directly to the file name specified in the "filename pattern".
export SBATCH_OUTPUT=		# Instruct Slurm to connect the batch script's standard output directly to the file name specified in the "filename pattern".
export SBATCH_ERROR=		# Instruct Slurm to connect the batch script's standard error directly to the file name specified in the "filename pattern".

export SLURM_ARRAY_JOB_ID=1       		# Job array’s master job ID number.
export SLURM_ARRAY_TASK_COUNT=26  		# Total number of tasks in a job array.
export SLURM_ARRAY_TASK_MAX=25    		# Job array’s maximum ID (index) number.
export SLURM_ARRAY_TASK_MIN=0    		# Job array’s minimum ID (index) number.
export SLURM_TASKS_PER_NODE=3    		# Job array’s master job ID number.
export SLURM_CPUS_PER_TASK=       		# Number of CPUs per task.
export SLURM_CPUS_ON_NODE=        		# Number of CPUs on the allocated node (actually pod).
export SLURM_JOB_CPUS_PER_NODE=   		# Count of processors available to the job on this node.
export SLURM_CPUS_PER_GPU=        		# Number of CPUs requested per allocated GPU.
export SLURM_MEM_PER_CPU=         	# Memory per CPU. Same as --mem-per-cpu .
export SLURM_MEM_PER_GPU=         	# Memory per GPU.
export SLURM_MEM_PER_NODE=        	# Memory per node. Same as --mem.
export SLURM_GPUS=                	# Number of GPUs requested (in total).
export SLURM_NTASKS=3              	# Same as -n, –ntasks. The number of tasks.
export SLURM_NTASKS_PER_NODE=3  		# Number of tasks requested per node.
export SLURM_NPROCS=$SLURM_NTASKS       	# Same as -n, --ntasks. See $SLURM_NTASKS.
export SLURM_NNODES=2            		# Total number of nodes (actually pods) in the job’s resource allocation.
# export SLURM_SUBMIT_DIR=/slurm        	# The path of the job submission directory.
# export SLURM_SUBMIT_HOST=$HOSTNAME       	# The hostname of the node used for job submission.
export SBATCH_JOB_NAME=				# Specified job name.

# To be supported later
# export SLURM_JOB_NODELIST=        # Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later.
# export SLURM_NODELIST=            # Deprecated. Same as SLURM_JOB_NODELIST. To be supported later.
# export SLURM_NTASKS_PER_SOCKET    # Number of tasks requested per socket. To be supported later.
# export SLURM_NTASKS_PER_CORE      # Number of tasks requested per core. To be supported later.
# export SLURM_NTASKS_PER_GPU       # Number of tasks requested per GPU. To be supported later.

# Calculated variables in runtime
export SLURM_JOB_ID=$(( JOB_COMPLETION_INDEX * SLURM_TASKS_PER_NODE + JOB_CONTAINER_INDEX + SLURM_ARRAY_JOB_ID ))   # The Job ID.
export SLURM_JOBID=$SLURM_JOB_ID                                                                                    # Deprecated. Same as $SLURM_JOB_ID
export SLURM_ARRAY_TASK_ID=${CONTAINER_INDEXES[${JOB_CONTAINER_INDEX}]}												# Task ID.

# fill_file_name fills file name by pattern
# \\ - Do not process any of the replacement symbols.
# %% - The character "%".
# %A - Job array's master job allocation number (for now it is equivalent to SLURM_JOB_ID).
# %a - Job array ID (index) number (SLURM_ARRAY_TASK_ID).
# %j - jobid of the running job (SLURM_JOB_ID).
# %N - short hostname (pod name).
# %n - node(pod) identifier relative to current job - index from K8S index job.
# %t - task identifier (rank) relative to current job - It is array id position.
# %u - user name (from the client machine).
# %x - job name.
fill_file_name () {
  REPLACED="$1"

  if [[ "$REPLACED" == "\\"* ]]; then
      REPLACED="${REPLACED//\\/}"
      echo "${REPLACED}"
      return 0
  fi

  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%A)/\1\n\2/g;:a s/(^|[^\n])%A/\1$SLURM_ARRAY_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%a)/\1\n\2/g;:a s/(^|[^\n])%a/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%j)/\1\n\2/g;:a s/(^|[^\n])%j/\1$SLURM_JOB_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%N)/\1\n\2/g;:a s/(^|[^\n])%N/\1$HOSTNAME/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%n)/\1\n\2/g;:a s/(^|[^\n])%n/\1$JOB_COMPLETION_INDEX/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%t)/\1\n\2/g;:a s/(^|[^\n])%t/\1$SLURM_ARRAY_TASK_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%u)/\1\n\2/g;:a s/(^|[^\n])%u/\1$USER_ID/;ta;s/\n//g")
  REPLACED=$(echo "$REPLACED" | sed -E "s/(%)(%x)/\1\n\2/g;:a s/(^|[^\n])%x/\1$SBATCH_JOB_NAME/;ta;s/\n//g")

  REPLACED="${REPLACED//%%/%}"

  echo "$REPLACED"
}

export SBATCH_INPUT=$(fill_file_name "$SBATCH_INPUT")
export SBATCH_OUTPUT=$(fill_file_name "$SBATCH_OUTPUT")
export SBATCH_ERROR=$(fill_file_name "$SBATCH_ERROR")

bash /slurm/script.sh
`,
							}).
							Obj(),
					},
				},
			},
			cmpopts: []cmp.Option{
				cmpopts.IgnoreFields(corev1.Volume{}, "Name"),
				cmpopts.IgnoreFields(corev1.LocalObjectReference{}, "Name"),
				cmpopts.IgnoreFields(corev1.VolumeMount{}, "Name"),
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
				if err := tc.beforeTest(&tc); err != nil {
					t.Error(err)
					return
				}
			}

			if tc.afterTest != nil {
				defer func() {
					if err := tc.afterTest(&tc); err != nil {
						t.Error(err)
					}
				}()
			}

			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			scheme := runtime.NewScheme()
			utilruntime.Must(k8sscheme.AddToScheme(scheme))
			utilruntime.Must(rayv1.AddToScheme(scheme))

			clientset := kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...)
			dynamicClient := fake.NewSimpleDynamicClient(scheme)
			restMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})

			for _, gvk := range tc.gvks {
				restMapper.Add(gvk, meta.RESTScopeNamespace)
			}

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(clientset).
				WithDynamicClient(dynamicClient).
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

				defaultCmpOpts := []cmp.Option{cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name")}
				opts := append(defaultCmpOpts, tc.cmpopts...)
				if diff := cmp.Diff(tc.wantLists[index], gotList, opts...); diff != "" {
					t.Errorf("Unexpected list for %s (-want/+got)\n%s", gvk.String(), diff)
				}
			}
		})
	}
}

func TestInteractivePod(t *testing.T) {
	testCases := map[string]struct {
		podName string
		options *CreateOptions
		pods    []runtime.Object
		wantErr string
	}{
		"success": {
			podName: "foo",
			options: &CreateOptions{
				Namespace:  "test",
				Attach:     &fakeRemoteAttach{},
				AttachFunc: testAttachFunc,
			},
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "test",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "bar",
								Stdin: true,
								TTY:   true,
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
		},
		"tty not allocated": {
			podName: "foo",
			options: &CreateOptions{
				Namespace:  "test",
				Attach:     &fakeRemoteAttach{},
				AttachFunc: testAttachFunc,
			},
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "test",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "bar",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			wantErr: "error: Unable to use a TTY - container bar did not allocate one",
		},
		"timeout waiting for pod": {
			podName: "foo",
			options: &CreateOptions{
				Namespace:         "test",
				Attach:            &fakeRemoteAttach{},
				AttachFunc:        testAttachFunc,
				PodRunningTimeout: 1 * time.Second,
			},
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "test",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "bar",
								Stdin: true,
								TTY:   true,
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
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

			clientset := k8sfake.NewSimpleClientset(tc.pods...)
			tcg := cmdtesting.NewTestClientGetter().WithK8sClientset(clientset)

			gotErr := tc.options.RunInteractivePod(context.TODO(), tcg, tc.podName)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}

type fakeRemoteAttach struct {
	url *url.URL
	err error
}

func (f *fakeRemoteAttach) Attach(url *url.URL, config *restclient.Config, stdin io.Reader, stdout, stderr io.Writer, tty bool, terminalSizeQueue remotecommand.TerminalSizeQueue) error {
	f.url = url
	return f.err
}

func testAttachFunc(o *CreateOptions, containerToAttach *corev1.Container, sizeQueue remotecommand.TerminalSizeQueue, pod *corev1.Pod) func() error {
	return func() error {
		u, err := url.Parse("http://kjobctl.test")
		if err != nil {
			return err
		}

		return o.Attach.Attach(u, nil, nil, nil, nil, o.TTY, sizeQueue)
	}
}
