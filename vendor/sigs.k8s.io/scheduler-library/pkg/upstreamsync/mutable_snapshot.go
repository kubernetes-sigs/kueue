// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstreamsync

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

/*

This is a proposal for exposing Add/RemovePod function on the snapshot.

This is needed because snapshot may track some of the state internally
(not presented here since we can't access unexported fields without copying a lot of code),
which may need to be updated together with the deletion or addition of the pod.

Snapshot already exposes AssumePod/ForgetPod, but it's specifically for pods that were not originally in the snapshot, which is useful in scheduling (i.e. "Assume" and then undo with "Forget").
We will want to expose AddPod/RemovePod for pods that were originally in the snapshot, which is useful in preemption (i.e. "Remove" and then undo with "Add").

Both cases can be unified under the same Add/Remove operations.

*/

type MutatingSnapshot struct {
	*cache.Snapshot
	addedPods   map[string]func()
	removedPods map[string]func()
}

func NewMutatingSnapshot(snapshot *cache.Snapshot) *MutatingSnapshot {
	return &MutatingSnapshot{
		Snapshot:    snapshot,
		addedPods:   map[string]func(){},
		removedPods: map[string]func(){},
	}
}

func (s *MutatingSnapshot) RemovePod(logger klog.Logger, podInfo *framework.PodInfo) error {
	key, err := framework.GetPodKey(podInfo.Pod)
	if err != nil {
		return err
	}

	ni, err := s.GetNodeByPod(podInfo.Pod)
	if err != nil {
		return err
	}

	err = ni.RemovePod(logger, podInfo.Pod)
	if err != nil {
		return err
	}

	if _, ok := s.addedPods[key]; ok {
		delete(s.addedPods, key)
	} else {
		s.removedPods[key] = func() { _ = s.AddPod(logger, podInfo) }
	}
	return nil
}

func (s *MutatingSnapshot) AddPod(logger klog.Logger, podInfo *framework.PodInfo) error {
	key, err := framework.GetPodKey(podInfo.Pod)
	if err != nil {
		return err
	}

	ni, err := s.GetNodeByPod(podInfo.Pod)
	if err != nil {
		return err
	}

	ni.AddPodInfo(podInfo)

	if _, ok := s.removedPods[key]; ok {
		delete(s.removedPods, key)
	} else {
		s.addedPods[key] = func() { _ = s.RemovePod(logger, podInfo) }
	}
	return nil
}

func (s *MutatingSnapshot) RestoreState() {
	for _, restoreFn := range s.addedPods {
		restoreFn()
	}
	for _, restoreFn := range s.removedPods {
		restoreFn()
	}

	s.addedPods = map[string]func(){}
	s.removedPods = map[string]func(){}
}

func (s *MutatingSnapshot) GetNodeByPod(pod *v1.Pod) (fwk.NodeInfo, error) {
	nodeName := pod.Spec.NodeName
	ni, err := s.Get(nodeName)
	if err != nil {
		return nil, err
	}
	return ni, nil
}
