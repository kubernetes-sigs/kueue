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

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

func TestRayJobBuilder(t *testing.T) {
	testStartTime := time.Now()
	userID := os.Getenv(constants.SystemEnvVarNameUser)

	testRayJobTemplateWrapper := wrappers.MakeRayJobTemplate("ray-job-template", metav1.NamespaceDefault).
		Label("foo", "bar").
		Annotation("foo", "baz").
		WithRayClusterSpec(wrappers.MakeRayClusterSpec().
			WithWorkerGroupSpec(
				*wrappers.MakeWorkerGroupSpec("g1").
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
					WithVolume("v2", "default-config2").
					Obj(),
			).
			WithWorkerGroupSpec(
				*wrappers.MakeWorkerGroupSpec("g2").
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
					WithVolume("v2", "default-config2").
					Obj(),
			).
			Obj(),
		)

	testCases := map[string]struct {
		namespace   string
		profile     string
		mode        v1alpha1.ApplicationProfileMode
		command     []string
		replicas    map[string]int
		minReplicas map[string]int
		maxReplicas map[string]int
		requests    corev1.ResourceList
		localQueue  string
		rayCluster  string
		kjobctlObjs []runtime.Object
		wantObj     []runtime.Object
		wantErr     error
	}{
		"shouldn't build ray job because template not found": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.RayJobMode,
			kjobctlObjs: []runtime.Object{
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.RayJobMode, Template: "ray-job-template"}).
					Obj(),
			},
			wantErr: apierrors.NewNotFound(schema.GroupResource{Group: "kjobctl.x-k8s.io", Resource: "rayjobtemplates"}, "ray-job-template"),
		},
		"should build ray job without replacements": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.RayJobMode,
			kjobctlObjs: []runtime.Object{
				testRayJobTemplateWrapper.Clone().Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.RayJobMode, Template: "ray-job-template"}).
					Obj(),
			},
			wantObj: []runtime.Object{
				wrappers.MakeRayJob("", metav1.NamespaceDefault).GenerateName("profile-").
					Annotation("foo", "baz").
					Label("foo", "bar").
					Label(constants.ProfileLabel, "profile").
					Spec(
						testRayJobTemplateWrapper.Clone().
							WithRayClusterSpec(
								wrappers.FromRayClusterSpec(*testRayJobTemplateWrapper.Clone().Template.Spec.RayClusterSpec).
									WithEnvVar(corev1.EnvVar{Name: constants.EnvVarNameUserID, Value: userID}).
									WithEnvVar(corev1.EnvVar{Name: constants.EnvVarTaskName, Value: "default_profile"}).
									WithEnvVar(corev1.EnvVar{
										Name:  constants.EnvVarTaskID,
										Value: fmt.Sprintf("%s_%s_default_profile", userID, testStartTime.Format(time.RFC3339)),
									}).
									WithEnvVar(corev1.EnvVar{Name: "PROFILE", Value: "default_profile"}).
									WithEnvVar(corev1.EnvVar{Name: "TIMESTAMP", Value: testStartTime.Format(time.RFC3339)}).
									Obj(),
							).
							Template.Spec,
					).
					Obj(),
			},
		},
		"should build ray job with replacements": {
			namespace:   metav1.NamespaceDefault,
			profile:     "profile",
			mode:        v1alpha1.RayJobMode,
			command:     []string{"sleep"},
			replicas:    map[string]int{"g1": 10, "g2": 20},
			minReplicas: map[string]int{"g1": 10, "g2": 20},
			maxReplicas: map[string]int{"g1": 15, "g2": 25},
			requests:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
			localQueue:  "lq1",
			kjobctlObjs: []runtime.Object{
				testRayJobTemplateWrapper.Clone().Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.RayJobMode, Template: "ray-job-template"}).
					WithVolumeBundleReferences("vb1", "vb2").
					Obj(),
				wrappers.MakeVolumeBundle("vb1", metav1.NamespaceDefault).
					WithVolume("v3", "config3").
					WithVolumeMount(corev1.VolumeMount{Name: "vm3", MountPath: "/etc/config3"}).
					WithEnvVar(corev1.EnvVar{Name: "e3", Value: "value3"}).
					Obj(),
				wrappers.MakeVolumeBundle("vb2", metav1.NamespaceDefault).Obj(),
			},
			wantObj: []runtime.Object{
				wrappers.MakeRayJob("", metav1.NamespaceDefault).GenerateName("profile-").
					Annotation("foo", "baz").
					Label("foo", "bar").
					Label(constants.ProfileLabel, "profile").
					Label(kueueconstants.QueueLabel, "lq1").
					Spec(
						testRayJobTemplateWrapper.Clone().
							Entrypoint("sleep").
							WithRayClusterSpec(
								wrappers.FromRayClusterSpec(*testRayJobTemplateWrapper.Clone().Template.Spec.RayClusterSpec).
									Replicas("g1", 10).
									Replicas("g2", 20).
									MinReplicas("g1", 10).
									MinReplicas("g2", 20).
									MaxReplicas("g1", 15).
									MaxReplicas("g2", 25).
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
									Obj(),
							).
							Obj().Template.Spec,
					).
					Obj(),
			},
		},
		"should build ray job with raycluster replacement": {
			namespace:  metav1.NamespaceDefault,
			profile:    "profile",
			mode:       v1alpha1.RayJobMode,
			command:    []string{"python /home/ray/samples/sample_code.py"},
			requests:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
			localQueue: "lq1",
			rayCluster: "rc1",
			kjobctlObjs: []runtime.Object{
				testRayJobTemplateWrapper.Clone().Obj(),
				wrappers.MakeApplicationProfile("profile", metav1.NamespaceDefault).
					WithSupportedMode(v1alpha1.SupportedMode{Name: v1alpha1.RayJobMode, Template: "ray-job-template"}).
					WithVolumeBundleReferences("vb1", "vb2").
					Obj(),
				wrappers.MakeVolumeBundle("vb1", metav1.NamespaceDefault).
					WithVolume("v3", "config3").
					WithVolumeMount(corev1.VolumeMount{Name: "vm3", MountPath: "/etc/config3"}).
					WithEnvVar(corev1.EnvVar{Name: "e3", Value: "value3"}).
					Obj(),
				wrappers.MakeVolumeBundle("vb2", metav1.NamespaceDefault).Obj(),
			},
			wantObj: wrappers.MakeRayJob("", metav1.NamespaceDefault).GenerateName("profile-").
				Annotation("foo", "baz").
				Label("foo", "bar").
				Label(constants.ProfileLabel, "profile").
				WithRayClusterLabelSelector("rc1").
				Entrypoint("python /home/ray/samples/sample_code.py").
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...))

			gotObjs, gotErr := NewBuilder(tcg, testStartTime).
				WithNamespace(tc.namespace).
				WithProfileName(tc.profile).
				WithModeName(tc.mode).
				WithCommand(tc.command).
				WithReplicas(tc.replicas).
				WithMinReplicas(tc.minReplicas).
				WithMaxReplicas(tc.maxReplicas).
				WithLocalQueue(tc.localQueue).
				WithRayCluster(tc.rayCluster).
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
