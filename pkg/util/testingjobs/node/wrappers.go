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

package node

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeWrapper wraps a Node.
type NodeWrapper struct {
	corev1.Node
}

// MakeNode creates a wrapper for a Node
func MakeNode(name string) *NodeWrapper {
	return &NodeWrapper{corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	},
	}
}

// Obj returns the inner Node.
func (n *NodeWrapper) Obj() *corev1.Node {
	return &n.Node
}

// Name updates the name of the node
func (n *NodeWrapper) Name(name string) *NodeWrapper {
	n.ObjectMeta.Name = name
	return n
}

// Label adds a label to the Node
func (n *NodeWrapper) Label(k, v string) *NodeWrapper {
	if n.Labels == nil {
		n.Labels = make(map[string]string)
	}
	n.Labels[k] = v
	return n
}

// StatusConditions appends the given status conditions to the Node.
func (n *NodeWrapper) StatusConditions(conditions ...corev1.NodeCondition) *NodeWrapper {
	n.Status.Conditions = append(n.Status.Conditions, conditions...)
	return n
}

// StatusAllocatable updates the allocatable resources of the Node.
func (n *NodeWrapper) StatusAllocatable(resourceList corev1.ResourceList) *NodeWrapper {
	n.Status.Allocatable = resourceList
	return n
}

// Taints appends the given taints to the Node.
func (n *NodeWrapper) Taints(taints ...corev1.Taint) *NodeWrapper {
	n.Spec.Taints = append(n.Spec.Taints, taints...)
	return n
}

// Ready sets the Node to a ready status condition
func (n *NodeWrapper) Ready() *NodeWrapper {
	n.StatusConditions(corev1.NodeCondition{
		Type:   corev1.NodeReady,
		Status: corev1.ConditionTrue,
	})
	return n
}

// NotReady sets the Node to a not ready status condition
func (n *NodeWrapper) NotReady() *NodeWrapper {
	n.StatusConditions(corev1.NodeCondition{
		Type:   corev1.NodeReady,
		Status: corev1.ConditionFalse,
	})
	return n
}
