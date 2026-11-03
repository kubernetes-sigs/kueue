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

package deployment

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestReconcile(t *testing.T) {
	const ns = "test-ns"

	appLabels := map[string]string{"app": "my-deploy"}

	makeDeployment := func(name string, paused bool, annotations map[string]string, image string) *appsv1.Deployment {
		return &appsv1.Deployment{
			Name:        name,
			Namespace:   ns,
			UID:         types.UID(name + "-uid"),
			Annotations: annotations,
			Spec: appsv1.DeploymentSpec{
				Paused: paused,
				Selector: &metav1.LabelSelector{
					MatchLabels: appLabels,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: appLabels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "c",
							Image: image,
						}},
					},
				},
			},
		}
	}

	makeRS := func(name string, deployUID types.UID, revision string, image string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			Name:      name,
			Namespace: ns,
			Labels:    appLabels,
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": revision,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       "Deployment",
				Name:       "my-deploy",
				UID:        deployUID,
				Controller: new(true),
			}},
			Spec: appsv1.ReplicaSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: appLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: appLabels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "c",
							Image: image,
						}},
					},
				},
			},
		}
	}

	tests := map[string]struct {
		gateEnabled bool
		objects     []client.Object
		wantPaused  bool
		wantAnnot   bool
	}{
		"no-op without PausedByKueueAnnotation": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", false, nil, "pause"),
			},
			wantPaused: false,
			wantAnnot:  false,
		},
		"no-op for user-paused Deployment": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, nil, "pause"),
			},
			wantPaused: true,
			wantAnnot:  false,
		},
		"no-op when Kueue-paused with no ReplicaSets": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "pause"),
			},
			wantPaused: true,
			wantAnnot:  true,
		},
		"no-op when Kueue-paused with matching template": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "pause"),
				makeRS("my-deploy-abc", "my-deploy-uid", "1", "pause"),
			},
			wantPaused: true,
			wantAnnot:  true,
		},
		"unpauses when template has drifted": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "nginx:latest"),
				makeRS("my-deploy-abc", "my-deploy-uid", "1", "pause"),
			},
			wantPaused: false,
			wantAnnot:  false,
		},
		"no-op when feature gate is off": {
			gateEnabled: false,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "nginx:latest"),
				makeRS("my-deploy-abc", "my-deploy-uid", "1", "pause"),
			},
			wantPaused: true,
			wantAnnot:  true,
		},
		"uses latest revision when multiple RSs exist": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "nginx:latest"),
				makeRS("my-deploy-old", "my-deploy-uid", "1", "old-image"),
				makeRS("my-deploy-new", "my-deploy-uid", "2", "nginx:latest"),
			},
			wantPaused: true,
			wantAnnot:  true,
		},
		"skips RS not owned by Deployment": {
			gateEnabled: true,
			objects: []client.Object{
				makeDeployment("my-deploy", true, map[string]string{ctrlconstants.PausedByKueueAnnotation: "true"}, "nginx:latest"),
				makeRS("other-rs", "other-deploy-uid", "1", "nginx:latest"),
			},
			wantPaused: true,
			wantAnnot:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DeploymentParentSuspension, tc.gateEnabled)

			ctx, _ := utiltesting.ContextWithLog(t)
			c := utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build()

			r := &Reconciler{client: c}
			req := reconcile.Request{Name: "my-deploy", Namespace: ns}

			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := &appsv1.Deployment{}
			if err := c.Get(ctx, req.NamespacedName, got); err != nil {
				t.Fatalf("failed to get Deployment: %v", err)
			}
			if got.Spec.Paused != tc.wantPaused {
				t.Errorf("Deployment.Spec.Paused = %v, want %v", got.Spec.Paused, tc.wantPaused)
			}
			_, hasAnnot := got.Annotations[ctrlconstants.PausedByKueueAnnotation]
			if hasAnnot != tc.wantAnnot {
				t.Errorf("PausedByKueueAnnotation present = %v, want %v", hasAnnot, tc.wantAnnot)
			}
		})
	}
}

func TestEqualIgnoreHash(t *testing.T) {
	base := corev1.PodTemplateSpec{
		Labels: map[string]string{"app": "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Image: "pause"}},
		},
	}

	withHash := base.DeepCopy()
	withHash.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = "abc123"

	different := base.DeepCopy()
	different.Spec.Containers[0].Image = "nginx"

	tests := map[string]struct {
		t1, t2 corev1.PodTemplateSpec
		want   bool
	}{
		"identical templates": {
			t1: base, t2: base,
			want: true,
		},
		"differ only in pod-template-hash": {
			t1: base, t2: *withHash,
			want: true,
		},
		"different image": {
			t1: base, t2: *different,
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := equalIgnoreHash(tc.t1, tc.t2); got != tc.want {
				t.Errorf("equalIgnoreHash() = %v, want %v", got, tc.want)
			}
		})
	}
}
