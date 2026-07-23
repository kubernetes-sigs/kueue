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

package util

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetRayClusterHeadPod(t *testing.T) {
	const (
		namespace      = "default"
		rayClusterName = "raycluster"
	)
	rayCluster := &rayv1.RayCluster{ObjectMeta: metav1.ObjectMeta{Name: rayClusterName, Namespace: namespace}}
	matchingHead := rayPod("matching-head", namespace, rayClusterName, rayv1.HeadNode)
	tests := map[string]struct {
		pods    []client.Object
		want    *corev1.Pod
		wantErr bool
	}{
		"matching head Pod": {
			pods: []client.Object{
				matchingHead,
				rayPod("other-cluster-head", namespace, "other-raycluster", rayv1.HeadNode),
				rayPod("matching-worker", namespace, rayClusterName, rayv1.WorkerNode),
			},
			want: matchingHead,
		},
		"no matching head Pod": {
			wantErr: true,
		},
		"multiple matching head Pods": {
			pods: []client.Object{
				matchingHead,
				rayPod("second-matching-head", namespace, rayClusterName, rayv1.HeadNode),
			},
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(tc.pods...).Build()
			got, err := GetRayClusterHeadPod(context.Background(), c, rayCluster)
			if tc.wantErr {
				if err == nil {
					t.Fatal("GetRayClusterHeadPod() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetRayClusterHeadPod() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetRayClusterHeadPod() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func rayPod(name, namespace, rayClusterName string, nodeType rayv1.RayNodeType) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				kuberayutils.RayClusterLabelKey:  rayClusterName,
				kuberayutils.RayNodeTypeLabelKey: string(nodeType),
			},
		},
	}
}
