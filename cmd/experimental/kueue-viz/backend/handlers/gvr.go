package handlers

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ClusterQueuesGVR defines the GroupVersionResource for ClusterQueues
func ClusterQueuesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusterqueues",
	}
}

// WorkloadsGVR defines the GroupVersionResource for Workloads
func WorkloadsGVR() schema.GroupVersionResource {
	workloadsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "workloads",
	}
	return workloadsGVR
}

// LocalQueuesGVR defines the GroupVersionResource  for LocalQueues
func LocalQueuesGVR() schema.GroupVersionResource {
	localQueuesGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "localqueues",
	}
	return localQueuesGVR
}

// CohortsGVR defines the GroupVersionResource for Cohorts
func CohortsGVR() schema.GroupVersionResource {
	cohortsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "cohorts",
	}
	return cohortsGVR
}

// ResourceFlavorsGVR defines the GroupVersionResource for ResourceFlavors
func ResourceFlavorsGVR() schema.GroupVersionResource {
	resourceFlavorsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "resourceflavors",
	}
	return resourceFlavorsGVR
}

// NodesGVR defines the GroupVersionResource for Nodes
func NodesGVR() schema.GroupVersionResource {
	nodeGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "nodes",
	}
	return nodeGVR
}

// EventsGVR defines the GroupVersionResource for Events
func EventsGVR() schema.GroupVersionResource {
	eventsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "events",
	}
	return eventsGVR
}

// PodsGVR defines the GroupVersionResource Pods
func PodsGVR() schema.GroupVersionResource {
	podsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	return podsGVR
}
