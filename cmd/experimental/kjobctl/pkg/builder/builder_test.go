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
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestBuilder(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		namespace   string
		profile     string
		mode        v1alpha1.ApplicationProfileMode
		kjobctlObjs []runtime.Object
		wantObj     runtime.Object
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
		"should build job": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeJobTemplate("job-template", metav1.NamespaceDefault).Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{
						Name:     v1alpha1.JobMode,
						Template: "job-template",
					}).
					Obj(),
			},
			wantObj: wrappers.MakeJob("", metav1.NamespaceDefault).GenerateName("profile-").
				Label(constants.ProfileLabel, "profile").
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(fake.NewSimpleClientset(tc.kjobctlObjs...))
			gotObjs, gotErr := NewBuilder(tcg, testStartTime).
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

			if diff := cmp.Diff(tc.wantObj, gotObjs); diff != "" {
				t.Errorf("Objects after build (-want,+got):\n%s", diff)
			}
		})
	}
}
