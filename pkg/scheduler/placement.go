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

package scheduler

import (
	"context"
	"fmt"
	"maps"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"sigs.k8s.io/scheduler-library/pkg/simulator"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
	"sigs.k8s.io/kueue/pkg/workload"
)

func initPlacementSimulator(ctx context.Context, restConfig *rest.Config) (*simulator.SchedulingSimulator, error) {
	readonlyClient, err := simulator.NewReadonlyClient(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating readonly client: %w", err)
	}
	sim, err := simulator.NewSchedulingSimulator(ctx, &schedulerapi.KubeSchedulerConfiguration{
		Profiles: []schedulerapi.KubeSchedulerProfile{{
			SchedulerName: "default-scheduler",
		}},
	}, readonlyClient, nil)
	if err != nil {
		return nil, fmt.Errorf("creating scheduling simulator: %w", err)
	}
	return sim, nil
}

func (s *Scheduler) buildPlacementSnapshot(ctx context.Context) (*snapshot.ClusterSnapshot, []string, error) {
	var nodes corev1.NodeList
	if err := s.client.List(ctx, &nodes); err != nil {
		return nil, nil, fmt.Errorf("listing nodes: %w", err)
	}

	var readyNodes []*corev1.Node
	var nodeNames []string
	for i := range nodes.Items {
		for _, cond := range nodes.Items[i].Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				readyNodes = append(readyNodes, &nodes.Items[i])
				nodeNames = append(nodeNames, nodes.Items[i].Name)
				break
			}
		}
	}

	snap, err := s.placementSimulator.NewClusterSnapshot(ctx, nil, readyNodes)
	if err != nil {
		return nil, nil, fmt.Errorf("creating cluster snapshot: %w", err)
	}
	return snap, nodeNames, nil
}

func (s *Scheduler) updateAssignmentForPlacement(
	ctx context.Context,
	log logr.Logger,
	wl *workload.Info,
	assignment *flavorassigner.Assignment,
	resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor,
) {
	if s.placementSnapshot == nil || assignment.RepresentativeMode() != flavorassigner.Fit {
		return
	}

	placement, err := s.placementSnapshot.MakePlacement(s.placementNodeNames)
	if err != nil {
		log.Error(err, "Failed to create placement from node names")
		return
	}

	txErr := s.placementSnapshot.Transaction(ctx, func() (snapshot.TransactionResult, error) {
		for i := range assignment.PodSets {
			psa := &assignment.PodSets[i]
			if psa.TopologyAssignment != nil {
				continue
			}

			ps := utilpodset.FindPodSetByName(wl.Obj.Spec.PodSets, psa.Name)
			if ps == nil {
				continue
			}
			tmpl := ps.Template.DeepCopy()
			applyFlavorConstraints(tmpl, psa, resourceFlavors)

			results, err := s.placementSnapshot.SchedulePodsByTemplate(
				ctx, tmpl, placement, int(psa.Count),
				snapshot.NewSchedulePodsByTemplateOptions(false),
			)
			if err != nil {
				psa.Status = *flavorassigner.NewStatus(fmt.Sprintf("placement check error for podset %s: %v", psa.Name, err))
				assignment.UpdateMode(psa.Name, flavorassigner.NoFit)
				return snapshot.Revert, nil
			}
			if len(results) < int(psa.Count) {
				psa.Status = *flavorassigner.NewStatus(fmt.Sprintf("placement infeasible for podset %s: can place %d of %d pods", psa.Name, len(results), psa.Count))
				assignment.UpdateMode(psa.Name, flavorassigner.NoFit)
				return snapshot.Revert, nil
			}
		}
		return snapshot.Revert, nil
	})
	if txErr != nil {
		log.Error(txErr, "Placement transaction failed")
	}
}

func applyFlavorConstraints(
	tmpl *corev1.PodTemplateSpec,
	psa *flavorassigner.PodSetAssignment,
	resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor,
) {
	seen := make(map[kueue.ResourceFlavorReference]bool, len(psa.Flavors))
	for _, fa := range psa.Flavors {
		if seen[fa.Name] {
			continue
		}
		seen[fa.Name] = true
		rf, ok := resourceFlavors[fa.Name]
		if !ok {
			continue
		}
		if tmpl.Spec.NodeSelector == nil {
			tmpl.Spec.NodeSelector = make(map[string]string)
		}
		maps.Copy(tmpl.Spec.NodeSelector, rf.Spec.NodeLabels)
		tmpl.Spec.Tolerations = append(tmpl.Spec.Tolerations, rf.Spec.Tolerations...)
	}
}

func placementEnabled() bool {
	return features.Enabled(features.GangSchedulingPlacement) &&
		features.Enabled(features.SchedulerLibraryIntegration)
}
