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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/constants"
	kueueconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

func TestJobBuilder(t *testing.T) {
	testJobTemplate := &v1alpha1.JobTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      "job-template",
			Labels:    map[string]string{"foo": "bar"},
		},
		Template: v1alpha1.JobTemplateSpec{
			Spec: batchv1.JobSpec{
				Parallelism: ptr.To[int32](1),
				Completions: ptr.To[int32](1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "c1",
								Command: []string{""},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
								Env: []corev1.EnvVar{
									{Name: "e1", Value: "default-value1"},
									{Name: "e2", Value: "default-value2"},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "vm1", MountPath: "/etc/default-config1"},
									{Name: "vm2", MountPath: "/etc/default-config2"},
								},
							},
							{
								Name:    "c2",
								Command: []string{""},
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("2"),
									},
								},
								Env: []corev1.EnvVar{
									{Name: "e1", Value: "default-value1"},
									{Name: "e2", Value: "default-value2"},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "vm1", MountPath: "/etc/default-config1"},
									{Name: "vm2", MountPath: "/etc/default-config2"},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "v1",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "default-config1",
										},
									},
								},
							},
							{
								Name: "v2",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "default-config2",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	testCases := map[string]struct {
		namespace   string
		profile     string
		mode        v1alpha1.ApplicationProfileMode
		command     []string
		parallelism *int32
		completions *int32
		requests    corev1.ResourceList
		localQueue  string
		kjobctlObjs []runtime.Object
		wantObj     runtime.Object
		wantErr     error
	}{
		"shouldn't build job because template not found": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				&v1alpha1.ApplicationProfile{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "profile",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: v1alpha1.ApplicationProfileSpec{
						SupportedModes: []v1alpha1.SupportedMode{{
							Name:     v1alpha1.JobMode,
							Template: "job-template",
						}},
					},
				},
			},
			wantErr: apierrors.NewNotFound(schema.GroupResource{Group: "kjobctl.x-k8s.io", Resource: "jobtemplates"}, "job-template"),
		},
		"should build job without replacements": {
			namespace: metav1.NamespaceDefault,
			profile:   "profile",
			mode:      v1alpha1.JobMode,
			kjobctlObjs: []runtime.Object{
				testJobTemplate,
				&v1alpha1.ApplicationProfile{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "profile",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: v1alpha1.ApplicationProfileSpec{
						SupportedModes: []v1alpha1.SupportedMode{{
							Name:     v1alpha1.JobMode,
							Template: "job-template",
						}},
					},
				},
			},
			wantObj: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Job",
					APIVersion: "batch/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "profile-",
					Namespace:    metav1.NamespaceDefault,
					Labels: map[string]string{
						constants.ProfileLabel: "profile",
					},
				},
				Spec: testJobTemplate.Template.Spec,
			},
		},
		"should build job with replacements": {
			namespace:   metav1.NamespaceDefault,
			profile:     "profile",
			mode:        v1alpha1.JobMode,
			command:     []string{"sleep"},
			parallelism: ptr.To[int32](2),
			completions: ptr.To[int32](3),
			requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("3"),
			},
			localQueue: "lq1",
			kjobctlObjs: []runtime.Object{
				testJobTemplate,
				&v1alpha1.ApplicationProfile{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "profile",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: v1alpha1.ApplicationProfileSpec{
						SupportedModes: []v1alpha1.SupportedMode{{
							Name:     v1alpha1.JobMode,
							Template: "job-template",
						}},
						VolumeBundles: []v1alpha1.VolumeBundleReference{"vb1", "vb2"},
					},
				},
				&v1alpha1.VolumeBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vb1",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: v1alpha1.VolumeBundleSpec{
						Volumes: []corev1.Volume{
							{
								Name: "v1",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "config1",
										},
									},
								},
							},
							{
								Name: "v3",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "config3",
										},
									},
								},
							},
						},
						ContainerVolumeMounts: []corev1.VolumeMount{
							{Name: "vm1", MountPath: "/etc/config1"},
							{Name: "vm3", MountPath: "/etc/config3"},
						},
						EnvVars: []corev1.EnvVar{
							{Name: "e1", Value: "value1"},
							{Name: "e3", Value: "value3"},
						},
					},
				},
				&v1alpha1.VolumeBundle{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vb2",
						Namespace: metav1.NamespaceDefault,
					},
					Spec: v1alpha1.VolumeBundleSpec{},
				},
			},
			wantObj: &batchv1.Job{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Job",
					APIVersion: "batch/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "profile-",
					Namespace:    metav1.NamespaceDefault,
					Labels: map[string]string{
						constants.ProfileLabel:    "profile",
						kueueconstants.QueueLabel: "lq1",
					},
				},
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](2),
					Completions: ptr.To[int32](3),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "c1",
									Command: []string{"sleep"},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("3"),
										},
									},
									Env: []corev1.EnvVar{
										{Name: "e1", Value: "value1"},
										{Name: "e2", Value: "default-value2"},
										{Name: "e3", Value: "value3"},
									},
									VolumeMounts: []corev1.VolumeMount{
										{Name: "vm1", MountPath: "/etc/config1"},
										{Name: "vm2", MountPath: "/etc/default-config2"},
										{Name: "vm3", MountPath: "/etc/config3"},
									},
								},
								{
									Name:    "c2",
									Command: []string{"sleep"},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("3"),
										},
									},
									Env: []corev1.EnvVar{
										{Name: "e1", Value: "value1"},
										{Name: "e2", Value: "default-value2"},
										{Name: "e3", Value: "value3"},
									},
									VolumeMounts: []corev1.VolumeMount{
										{Name: "vm1", MountPath: "/etc/config1"},
										{Name: "vm2", MountPath: "/etc/default-config2"},
										{Name: "vm3", MountPath: "/etc/config3"},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "v1",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "config1",
											},
										},
									},
								},
								{
									Name: "v2",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "default-config2",
											},
										},
									},
								},
								{
									Name: "v3",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "config3",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: nil,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tcg := cmdtesting.NewTestClientGetter().
				WithKjobctlClientset(kjobctlfake.NewSimpleClientset(tc.kjobctlObjs...))

			gotObjs, gotErr := NewBuilder(tcg).
				WithNamespace(tc.namespace).
				WithProfileName(tc.profile).
				WithModeName(tc.mode).
				WithCommand(tc.command).
				WithParallelism(tc.parallelism).
				WithCompletions(tc.completions).
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
