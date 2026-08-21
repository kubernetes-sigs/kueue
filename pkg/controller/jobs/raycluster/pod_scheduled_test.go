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

package raycluster

import (
	"testing"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func scheduledPod(name, namespace string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
		},
	}
}

func TestPodsScheduledForRayCluster(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const (
		ns          = "ns"
		clusterName = "ray"
	)

	headLabels := map[string]string{
		rayutils.RayClusterLabelKey:  clusterName,
		rayutils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
	}
	worker1Labels := map[string]string{
		rayutils.RayClusterLabelKey:   clusterName,
		rayutils.RayNodeTypeLabelKey:  string(rayv1.WorkerNode),
		rayutils.RayNodeGroupLabelKey: "workers",
	}
	worker2Labels := map[string]string{
		rayutils.RayClusterLabelKey:   clusterName,
		rayutils.RayNodeTypeLabelKey:  string(rayv1.WorkerNode),
		rayutils.RayNodeGroupLabelKey: "workers2",
	}

	podSets := []kueue.PodSet{
		{Name: headGroupPodSetName, Count: 1},
		{Name: kueue.NewPodSetReference("workers"), Count: 2},
		{Name: kueue.NewPodSetReference("workers2"), Count: 1},
	}

	cases := map[string]struct {
		objects []client.Object
		want    bool
	}{
		"all pod sets scheduled": {
			objects: []client.Object{
				scheduledPod("head", ns, headLabels),
				scheduledPod("w1", ns, worker1Labels),
				scheduledPod("w2", ns, worker1Labels),
				scheduledPod("w3", ns, worker2Labels),
			},
			want: true,
		},
		"aggregate count met but worker group short": {
			objects: []client.Object{
				scheduledPod("head", ns, headLabels),
				scheduledPod("w1", ns, worker1Labels),
				scheduledPod("w2", ns, worker2Labels),
				scheduledPod("w3", ns, worker2Labels),
			},
			want: false,
		},
		"empty cluster name": {
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cl := fake.NewClientBuilder().WithObjects(tc.objects...).Build()
			cluster := clusterName
			if name == "empty cluster name" {
				cluster = ""
			}
			got, err := PodsScheduledForRayCluster(ctx, cl, ns, cluster, podSets)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("PodsScheduledForRayCluster() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsScheduledForRayClusterFromSpec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	spec := &rayv1.RayClusterSpec{
		HeadGroupSpec: rayv1.HeadGroupSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}},
			},
		},
		WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
			{
				GroupName: "workers",
				Replicas:  new(int32(2)),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}},
				},
			},
		},
	}
	podSets, err := BuildPodSets(spec, nil)
	if err != nil {
		t.Fatalf("BuildPodSets: %v", err)
	}

	headLabels := map[string]string{
		rayutils.RayClusterLabelKey:  "ray",
		rayutils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
	}
	workerLabels := map[string]string{
		rayutils.RayClusterLabelKey:   "ray",
		rayutils.RayNodeTypeLabelKey:  string(rayv1.WorkerNode),
		rayutils.RayNodeGroupLabelKey: "workers",
	}
	cl := fake.NewClientBuilder().WithObjects(
		scheduledPod("head", "ns", headLabels),
		scheduledPod("w1", "ns", workerLabels),
		scheduledPod("w2", "ns", workerLabels),
	).Build()

	got, err := PodsScheduledForRayCluster(ctx, cl, "ns", "ray", podSets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected all pod sets to be scheduled")
	}
}
