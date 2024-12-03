package handlers

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Define the GroupVersionResource for ResourceFlavors
func ResourceFlavorGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "resourceflavors",
	}
}

// Define the GroupVersionResource for ClusterQueues
func ClusterQueueGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusterqueues",
	}
}

func WorkloadsGVR() schema.GroupVersionResource {
	workloadsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "workloads",
	}
	return workloadsGVR
}

func LocalQueuesGVR() schema.GroupVersionResource {
	localQueuesGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "localqueues",
	}
	return localQueuesGVR
}

func CohortsGVR() schema.GroupVersionResource {
	cohortsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "cohorts",
	}
	return cohortsGVR
}

func ResourceFlavorsGVR() schema.GroupVersionResource {
	resourceFlavorsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "resourceflavors",
	}
	return resourceFlavorsGVR
}

func NodeGVR() schema.GroupVersionResource {
	nodeGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "nodes",
	}
	return nodeGVR
}

func EventsGVR() schema.GroupVersionResource {
	eventsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "events",
	}
	return eventsGVR
}

func PodsGVR() schema.GroupVersionResource {
	podsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	return podsGVR
}
