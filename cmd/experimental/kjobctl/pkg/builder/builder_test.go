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

package builder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestBuilder(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		namespace   string
		profile     string
		mode        v1alpha1.ApplicationProfileMode
		kjobctlObjs []runtime.Object
		wantRootObj runtime.Object
		wantErr     error
	}{
		"shouldn't build job because no namespace specified": {
			wantErr: noNamespaceSpecifiedErr,
		},
		"shouldn't build job because no application profile specified": {
			namespace: metav1.NamespaceDefault,
			wantErr:   noApplicationProfileSpecifiedErr,
		},
		"shouldn't build job because application profile not found": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			wantErr:   apierrors.NewNotFound(schema.GroupResource{Group: "kjobctl.x-k8s.io", Resource: "applicationprofiles"}, "profile"),
		},
		"shouldn't build job because no application profile mode specified": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.JobMode}).
					Obj(),
			},
			wantErr: noApplicationProfileModeSpecifiedErr,
		},
		"shouldn't build job because application profile mode not configured": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.InteractiveMode}).
					Obj(),
			},
			wantErr: applicationProfileModeNotConfiguredErr,
		},
		"shouldn't build job because invalid application profile mode": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      "Invalid",
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.InteractiveMode}).
					Obj(),
			},
			wantErr: invalidApplicationProfileModeErr,
		},
		"shouldn't build job because command not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.CmdFlag},
					}).
					Obj(),
			},
			wantErr: noCommandSpecifiedErr,
		},
		"shouldn't build job because parallelism not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.ParallelismFlag},
					}).
					Obj(),
			},
			wantErr: noParallelismSpecifiedErr,
		},
		"shouldn't build job because completions not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.CompletionsFlag},
					}).
					Obj(),
			},
			wantErr: noCompletionsSpecifiedErr,
		},
		"shouldn't build job because replicas not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.ReplicasFlag},
					}).
					Obj(),
			},
			wantErr: noReplicasSpecifiedErr,
		},
		"shouldn't build job because min-replicas not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.MinReplicasFlag},
					}).
					Obj(),
			},
			wantErr: noMinReplicasSpecifiedErr,
		},
		"shouldn't build job because max-replicas not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.MaxReplicasFlag},
					}).
					Obj(),
			},
			wantErr: noMaxReplicasSpecifiedErr,
		},
		"shouldn't build job because request not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.RequestFlag},
					}).
					Obj(),
			},
			wantErr: noRequestsSpecifiedErr,
		},
		"shouldn't build job because local queue not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.JobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.LocalQueueFlag},
					}).
					Obj(),
			},
			wantErr: noLocalQueueSpecifiedErr,
		},
		"shouldn't build job because raycluster not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.RayJobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.RayJobMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.RayClusterFlag},
					}).
					Obj(),
			},
			wantErr: noRayClusterSpecifiedErr,
		},
		"shouldn't build job because array not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.ArrayFlag},
					}).
					Obj(),
			},
			wantErr: noArraySpecifiedErr,
		},
		"shouldn't build job because cpusPerTask not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.CpusPerTaskFlag},
					}).
					Obj(),
			},
			wantErr: noCpusPerTaskSpecifiedErr,
		},
		"shouldn't build job because error not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.ErrorFlag},
					}).
					Obj(),
			},
			wantErr: noErrorSpecifiedErr,
		},
		"shouldn't build job because gpusPerTask not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.GpusPerTaskFlag},
					}).
					Obj(),
			},
			wantErr: noGpusPerTaskSpecifiedErr,
		},
		"shouldn't build job because input not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.InputFlag},
					}).
					Obj(),
			},
			wantErr: noInputSpecifiedErr,
		},
		"shouldn't build job because jobName not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.JobNameFlag},
					}).
					Obj(),
			},
			wantErr: noJobNameSpecifiedErr,
		},
		"shouldn't build job because memPerCPU not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.MemPerCPUFlag},
					}).
					Obj(),
			},
			wantErr: noMemPerCPUSpecifiedErr,
		},
		"shouldn't build job because memPerGPU not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.MemPerGPUFlag},
					}).
					Obj(),
			},
			wantErr: noMemPerGPUSpecifiedErr,
		},
		"shouldn't build job because memPerTask not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.MemPerTaskFlag},
					}).
					Obj(),
			},
			wantErr: noMemPerTaskSpecifiedErr,
		},
		"shouldn't build job because nodes not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.NodesFlag},
					}).
					Obj(),
			},
			wantErr: noNodesSpecifiedErr,
		},
		"shouldn't build job because nTasks not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.NTasksFlag},
					}).
					Obj(),
			},
			wantErr: noNTasksSpecifiedErr,
		},
		"shouldn't build job because output not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.OutputFlag},
					}).
					Obj(),
			},
			wantErr: noOutputSpecifiedErr,
		},
		"shouldn't build job because partition not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.PartitionFlag},
					}).
					Obj(),
			},
			wantErr: noPartitionSpecifiedErr,
		},
		"shouldn't build job because priority not specified with required flags": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.SlurmMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:          v1alpha1.SlurmMode,
						RequiredFlags: []v1alpha1.Flag{v1alpha1.PriorityFlag},
					}).
					Obj(),
			},
			wantErr: noPrioritySpecifiedErr,
		},
		"should build job": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).
					Label("foo", "bar").
					Annotation("foo", "baz").
					Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:     v1alpha1.JobMode,
						Template: "job-template",
					}).
					Obj(),
			},
			wantRootObj: wrappers.MakeJob("", metav1.NamespaceDefault).GenerateName("profile-job-").
				Annotation("foo", "baz").
				Label("foo", "bar").
				Profile("profile").
				Mode(v1alpha1.JobMode).
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(fake.NewSimpleClientset(tc.kjobctlObjs...))
			gotRootObj, gotChildObjs, gotErr := NewBuilder(tcg, testStartTime).
				WithNamespace(tc.namespace).
				WithProfileName(tc.profile).
				WithModeName(tc.mode).
				Do(ctx)

			var opts []cmp.Option
			var statusError *apierrors.StatusError
			if !errors.As(tc.wantErr, &statusError) {
				opts = append(opts, cmpopts.EquateErrors())
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, opts...); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				return
			}

			if diff := cmp.Diff(tc.wantRootObj, gotRootObj, opts...); diff != "" {
				t.Errorf("Root object after build (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff([]runtime.Object(nil), gotChildObjs, opts...); diff != "" {
				t.Errorf("Child objects after build (-want,+got):\n%s", diff)
			}
		})
	}
}
