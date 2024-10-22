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

package flavorassigner

import (
	"errors"
	"fmt"

	"github.com/go-logr/logr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/workload"
)

func assignTopology(log logr.Logger,
	psAssignment *PodSetAssignment,
	cq *cache.ClusterQueueSnapshot,
	psResources workload.PodSetResources,
	podSet *kueue.PodSet) {
	switch {
	case psAssignment.Status.IsError():
		log.Info("There is no resource quota assignment for the workload. No need to check TAS.", "message", psAssignment.Status.Message())
	case len(cq.TASFlavorSnapshots) == 0:
		if psAssignment.Status == nil {
			psAssignment.Status = &Status{}
		}
		psAssignment.Status.append("Workload requires Topology, but there is no TAS cache information")
		psAssignment.Flavors = nil
	default:
		singlePodRequests := psResources.Requests.Clone()
		singlePodRequests.Divide(int64(psResources.Count))
		podCount := psAssignment.Count
		tasFlvr, err := onlyFlavor(psAssignment.Flavors)
		if err != nil {
			if psAssignment.Status == nil {
				psAssignment.Status = &Status{}
			}
			psAssignment.Status.err = err
			psAssignment.Flavors = nil
			return
		} else {
			snapshot := cq.TASFlavorSnapshots[*tasFlvr]
			if snapshot == nil {
				if psAssignment.Status == nil {
					psAssignment.Status = &Status{}
				}
				psAssignment.Status.append("Workload requires Topology, but there is no TAS cache information for the assigned flavor")
				psAssignment.Flavors = nil
				return
			}
			psAssignment.TopologyAssignment = snapshot.FindTopologyAssignment(podSet.TopologyRequest,
				singlePodRequests, podCount)
			if psAssignment.TopologyAssignment == nil {
				if psAssignment.Status == nil {
					psAssignment.Status = &Status{}
				}
				psAssignment.Status.append("Workload cannot fit within the TAS ResourceFlavor")
				psAssignment.Flavors = nil
			}
			log.Info("TAS PodSet assignment", "tasAssignment", psAssignment.TopologyAssignment)
		}
	}
}

func onlyFlavor(ra ResourceAssignment) (*kueue.ResourceFlavorReference, error) {
	var result *kueue.ResourceFlavorReference
	for _, v := range ra {
		if result == nil {
			result = &v.Name
		} else if *result != v.Name {
			return nil, fmt.Errorf("more than one flavor assigned: %s, %s", v.Name, *result)
		}
	}
	if result != nil {
		return result, nil
	}
	return nil, errors.New("no flavor assigned")
}
