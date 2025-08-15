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

package tas

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodeSchedulingPropertiesChanged(t *testing.T) {
	now := metav1.Now()
	later := metav1.NewTime(now.Add(10 * time.Second))

	baseNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"topology.kubernetes.io/zone": "zone-a",
				"node-role":                   "worker",
			},
			Annotations: map[string]string{
				"test-annotation": "value",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: false,
			Taints: []corev1.Taint{
				{
					Key:    "test-taint",
					Value:  "value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("32Gi"),
			},
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastHeartbeatTime:  now,
					LastTransitionTime: now,
				},
				{
					Type:               corev1.NodeMemoryPressure,
					Status:             corev1.ConditionFalse,
					LastHeartbeatTime:  now,
					LastTransitionTime: now,
				},
			},
		},
	}

	testCases := []struct {
		name        string
		oldNode     *corev1.Node
		newNode     *corev1.Node
		wantChanged bool
	}{
		{
			name:    "LastHeartbeatTime changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Conditions[0].LastHeartbeatTime = later
				n.Status.Conditions[1].LastHeartbeatTime = later
				return n
			}(),
			wantChanged: false,
		},
		{
			name:    "Annotation changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Annotations["new-annotation"] = "new-value"
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Label changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Labels["new-label"] = "new-value"
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Node Ready status changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Conditions[0].Status = corev1.ConditionFalse
				n.Status.Conditions[0].LastTransitionTime = later
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Allocatable resources changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("16")
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Taints changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{
					Key:    "new-taint",
					Value:  "new-value",
					Effect: corev1.TaintEffectNoExecute,
				})
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Unschedulable changed",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Spec.Unschedulable = true
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "Update Multiple properties",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Conditions[0].LastHeartbeatTime = later
				n.Status.Conditions[1].LastHeartbeatTime = later
				n.Annotations["another-annotation"] = "another-value"
				n.ResourceVersion = "12345"
				return n
			}(),
			wantChanged: true,
		},
		{
			name:    "New condition type added",
			oldNode: baseNode.DeepCopy(),
			newNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{
					Type:               corev1.NodeDiskPressure,
					Status:             corev1.ConditionTrue,
					LastHeartbeatTime:  now,
					LastTransitionTime: now,
				})
				return n
			}(),
			wantChanged: true,
		},
		{
			name: "Condition removed",
			oldNode: func() *corev1.Node {
				n := baseNode.DeepCopy()
				n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{
					Type:               corev1.NodeDiskPressure,
					Status:             corev1.ConditionFalse,
					LastHeartbeatTime:  now,
					LastTransitionTime: now,
				})
				return n
			}(),
			newNode:     baseNode.DeepCopy(),
			wantChanged: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNodeSchedulingPropertiesChanged(tc.oldNode, tc.newNode)
			if got != tc.wantChanged {
				t.Errorf("nodeSchedulingPropertiesChanged() = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}
