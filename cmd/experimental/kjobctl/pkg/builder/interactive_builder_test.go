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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

func TestInteractiveBuilder(t *testing.T) {
	testStartTime := time.Now()
	userID := os.Getenv(constants.SystemEnvVarNameUser)

	testPodTemplateWrapper := wrappers.MakePodTemplate("pod-template", metav1.NamespaceDefault).
		WithInitContainer(
			*wrappers.MakeContainer("ic1", "").
				WithEnvVar(corev1.EnvVar{Name: "e0", Value: "default-value0"}).
				WithVolumeMount(corev1.VolumeMount{Name: "vm0", MountPath: "/etc/default-config0"}).
				Obj(),
		).
		WithContainer(
			*wrappers.MakeContainer("c1", "").
				WithRequest(corev1.ResourceCPU, resource.MustParse("1")).
				WithEnvVar(corev1.EnvVar{Name: "e1", Value: "default-value1"}).
				WithEnvVar(corev1.EnvVar{Name: "e2", Value: "default-value2"}).
				WithVolumeMount(corev1.VolumeMount{Name: "vm1", MountPath: "/etc/default-config1"}).
				WithVolumeMount(corev1.VolumeMount{Name: "vm2", MountPath: "/etc/default-config2"}).
				Obj(),
		).
		WithContainer(*wrappers.MakeContainer("c2", "").
			WithRequest(corev1.ResourceCPU, resource.MustParse("2")).
			WithEnvVar(corev1.EnvVar{Name: "e1", Value: "default-value1"}).
			WithEnvVar(corev1.EnvVar{Name: "e2", Value: "default-value2"}).
			WithVolumeMount(corev1.VolumeMount{Name: "vm1", MountPath: "/etc/default-config1"}).
			WithVolumeMount(corev1.VolumeMount{Name: "vm2", MountPath: "/etc/default-config2"}).
			Obj(),
		).
		WithVolume("v1", "default-config1").
		WithVolume("v2", "default-config2")

	testCases := map[string]struct {
		namespace   string
		profile     string
		mode        v1alpha1.ApplicationProfileMode
		command     []string
		requests    corev1.ResourceList
		localQueue  string
		k8sObjs     []runtime.Object
		kjobctlObjs []runtime.Object
		wantObj     runtime.Object
		wantErr     error
	}{
		"shouldn't build pod because template not found": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.InteractiveMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.InteractiveMode, Template: "pod-template"}).
					Obj(),
			},
			wantErr: apierrors.NewNotFound(schema.GroupResource{Resource: "podtemplates"}, "pod-template"),
		},
		"should build job without replacements": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.InteractiveMode,
			k8sObjs:   []runtime.Object{testPodTemplateWrapper.Clone().Obj()},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.InteractiveMode, Template: "pod-template"}).
					Obj(),
			},
			wantObj: wrappers.MakePod("", metav1.NamespaceDefault).GenerateName("profile-").
				Label(constants.ProfileLabel, "profile").
				Spec(
					testPodTemplateWrapper.Clone().
						WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
						WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
						WithEnvVar(corev1.EnvVar{
							Name:  constants.EnvVarTaskID,
							Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
						}).
						WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
						WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
						TTY().
						Stdin().
						Obj().Template.Spec,
				).
				Obj(),
		},
		"should build job with replacements": {
			namespace:  metav1.NamespaceDefault,
			profile:    "profile",
			mode:       v1alpha1.InteractiveMode,
			command:    []string{"sleep"},
			requests:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
			localQueue: "lq1",
			k8sObjs:    []runtime.Object{testPodTemplateWrapper.Clone().Obj()},
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.InteractiveMode, Template: "pod-template"}).
					WithVolumeBundleReferences("vb1", "vb2").
					Obj(),
				wrappers.MakeVolumeBundle("vb1", metav1.NamespaceDefault).
					WithVolume("v3", "config3").
					WithVolumeMount(corev1.VolumeMount{Name: "vm3", MountPath: "/etc/config3"}).
					WithEnvVar(corev1.EnvVar{Name: "e3", Value: "value3"}).
					Obj(),
				wrappers.MakeVolumeBundle("vb2", metav1.NamespaceDefault).Obj(),
			},
			wantObj: wrappers.MakePod("", metav1.NamespaceDefault).GenerateName("profile-").
				Label(constants.ProfileLabel, "profile").
				Label(kueueconstants.QueueLabel, "lq1").
				Spec(
					testPodTemplateWrapper.Clone().
						Command([]string{"sleep"}).
						WithRequest(corev1.ResourceCPU, resource.MustParse("3")).
						WithVolume("v3", "config3").
						WithVolumeMount(corev1.VolumeMount{Name: "vm3", MountPath: "/etc/config3"}).
						WithEnvVar(corev1.EnvVar{Name: "e3", Value: "value3"}).
						WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
						WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
						WithEnvVar(corev1.EnvVar{
							Name:  constants.EnvVarTaskID,
							Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
						}).
						WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
						WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
						TTY().
						Stdin().
						Obj().Template.Spec,
				).
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...)).
				WithK8sClientset(k8sfake.NewSimpleClientset(tc.k8sObjs...))

			gotObjs, gotErr := NewBuilder(tcg, testStartTime).
				WithNamespace(tc.namespace).
				WithProfileName(tc.profile).
				WithModeName(tc.mode).
				WithCommand(tc.command).
				WithRequests(tc.requests).
				WithLocalQueue(tc.localQueue).
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
