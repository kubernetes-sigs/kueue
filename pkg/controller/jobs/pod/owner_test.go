/*
Copyright The Kubernetes Authors.

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

package pod

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestFindParentDeployment(t *testing.T) {
	const ns = "test-ns"

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy",
			Namespace: ns,
			UID:       "deploy-uid",
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy-abc123",
			Namespace: ns,
			UID:       "rs-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "my-deploy",
				UID:        "deploy-uid",
			}},
		},
	}

	tests := map[string]struct {
		pod      *corev1.Pod
		objects  []client.Object
		wantName string
		wantNil  bool
		wantErr  bool
	}{
		"pod without SuspendedByParentAnnotation returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
				},
			},
			wantNil: true,
		},
		"pod with non-deployment SuspendedByParentAnnotation returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "statefulset",
					},
				},
			},
			wantNil: true,
		},
		"pod with no owner references returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
				},
			},
			wantNil: true,
		},
		"pod with non-ReplicaSet owner returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "batch/v1",
						Kind:       "Job",
						Name:       "my-job",
					}},
				},
			},
			wantNil: true,
		},
		"pod whose ReplicaSet does not exist returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "deleted-rs",
					}},
				},
			},
			wantNil: true,
		},
		"ReplicaSet with no Deployment owner returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       "orphan-rs",
					}},
				},
			},
			objects: []client.Object{
				&appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphan-rs",
						Namespace: ns,
					},
				},
			},
			wantNil: true,
		},
		"Deployment does not exist returns nil": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       rs.Name,
					}},
				},
			},
			objects: []client.Object{rs},
			wantNil: true,
		},
		"full chain Pod -> ReplicaSet -> Deployment returns the Deployment": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pod",
					Namespace: ns,
					Annotations: map[string]string{
						podconstants.SuspendedByParentAnnotation: "deployment",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "ReplicaSet",
						Name:       rs.Name,
					}},
				},
			},
			objects:  []client.Object{rs, deploy},
			wantName: "my-deploy",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().WithObjects(tc.objects...)
			c := builder.Build()

			got, err := findParentDeployment(ctx, c, tc.pod)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %s", got.Name)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil Deployment, got nil")
			}
			if got.Name != tc.wantName {
				t.Errorf("got Deployment %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}

func TestResumeParent(t *testing.T) {
	const ns = "test-ns"

	makeChain := func(deployAnnotations map[string]string, paused bool) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "my-deploy",
				Namespace:   ns,
				UID:         "deploy-uid",
				Annotations: deployAnnotations,
			},
			Spec: appsv1.DeploymentSpec{
				Paused: paused,
			},
		}
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-deploy-abc123",
				Namespace: ns,
				UID:       "rs-uid",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "my-deploy",
					UID:        "deploy-uid",
				}},
			},
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-pod",
				Namespace: ns,
				Annotations: map[string]string{
					podconstants.SuspendedByParentAnnotation: "deployment",
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       rs.Name,
				}},
			},
		}
		return deploy, rs, pod
	}

	tests := map[string]struct {
		setup      func() (*Pod, []client.Object)
		wantPaused bool
		wantAnnot  bool
	}{
		"unpauses Deployment paused by Kueue": {
			setup: func() (*Pod, []client.Object) {
				deploy, rs, pod := makeChain(
					map[string]string{controllerconstants.PausedByKueueAnnotation: "true"},
					true,
				)
				return &Pod{pod: *pod}, []client.Object{deploy, rs}
			},
			wantPaused: false,
			wantAnnot:  false,
		},
		"does not unpause Deployment without Kueue annotation": {
			setup: func() (*Pod, []client.Object) {
				deploy, rs, pod := makeChain(nil, true)
				return &Pod{pod: *pod}, []client.Object{deploy, rs}
			},
			wantPaused: true,
			wantAnnot:  false,
		},
		"no-op for non-Deployment pod": {
			setup: func() (*Pod, []client.Object) {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "standalone-pod",
						Namespace: ns,
					},
				}
				return &Pod{pod: *pod}, nil
			},
			wantPaused: false,
			wantAnnot:  false,
		},
		"handles pod group using first pod": {
			setup: func() (*Pod, []client.Object) {
				deploy, rs, pod := makeChain(
					map[string]string{controllerconstants.PausedByKueueAnnotation: "true"},
					true,
				)
				return &Pod{
					isGroup: true,
					list:    corev1.PodList{Items: []corev1.Pod{*pod}},
				}, []client.Object{deploy, rs}
			},
			wantPaused: false,
			wantAnnot:  false,
		},
		"empty pod group is a no-op": {
			setup: func() (*Pod, []client.Object) {
				return &Pod{
					isGroup: true,
					list:    corev1.PodList{},
				}, nil
			},
			wantPaused: false,
			wantAnnot:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			p, objects := tc.setup()

			builder := utiltesting.NewClientBuilder().WithObjects(objects...)
			c := builder.Build()

			if err := p.ResumeParent(ctx, c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Only verify Deployment state if one exists in the test objects.
			for _, obj := range objects {
				if deploy, ok := obj.(*appsv1.Deployment); ok {
					got := &appsv1.Deployment{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(deploy), got); err != nil {
						t.Fatalf("failed to get Deployment: %v", err)
					}
					if got.Spec.Paused != tc.wantPaused {
						t.Errorf("Deployment.Spec.Paused = %v, want %v", got.Spec.Paused, tc.wantPaused)
					}
					_, hasAnnot := got.Annotations[controllerconstants.PausedByKueueAnnotation]
					if hasAnnot != tc.wantAnnot {
						t.Errorf("PausedByKueueAnnotation present = %v, want %v", hasAnnot, tc.wantAnnot)
					}
					break
				}
			}
		})
	}
}

func TestSuspendParent(t *testing.T) {
	const ns = "test-ns"

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy",
			Namespace: ns,
			UID:       "deploy-uid",
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-deploy-abc123",
			Namespace: ns,
			UID:       "rs-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "my-deploy",
				UID:        "deploy-uid",
			}},
		},
	}
	deploymentPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pod",
			Namespace: ns,
			Annotations: map[string]string{
				podconstants.SuspendedByParentAnnotation: "deployment",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       rs.Name,
			}},
		},
	}

	tests := map[string]struct {
		setup      func() (*Pod, []client.Object)
		wantPaused bool
		wantAnnot  bool
	}{
		"pauses parent Deployment": {
			setup: func() (*Pod, []client.Object) {
				return &Pod{pod: *deploymentPod}, []client.Object{deploy.DeepCopy(), rs}
			},
			wantPaused: true,
			wantAnnot:  true,
		},
		"skips already-paused Deployment": {
			setup: func() (*Pod, []client.Object) {
				d := deploy.DeepCopy()
				d.Spec.Paused = true
				return &Pod{pod: *deploymentPod}, []client.Object{d, rs}
			},
			wantPaused: true,
			wantAnnot:  false,
		},
		"no-op for non-Deployment pod": {
			setup: func() (*Pod, []client.Object) {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: ns}}
				return &Pod{pod: *pod}, nil
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			p, objects := tc.setup()
			builder := utiltesting.NewClientBuilder().WithObjects(objects...)
			c := builder.Build()

			if err := p.SuspendParent(ctx, c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, obj := range objects {
				if d, ok := obj.(*appsv1.Deployment); ok {
					got := &appsv1.Deployment{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(d), got); err != nil {
						t.Fatalf("failed to get Deployment: %v", err)
					}
					if got.Spec.Paused != tc.wantPaused {
						t.Errorf("Deployment.Spec.Paused = %v, want %v", got.Spec.Paused, tc.wantPaused)
					}
					_, hasAnnot := got.Annotations[controllerconstants.PausedByKueueAnnotation]
					if hasAnnot != tc.wantAnnot {
						t.Errorf("PausedByKueueAnnotation present = %v, want %v", hasAnnot, tc.wantAnnot)
					}
					break
				}
			}
		})
	}
}
