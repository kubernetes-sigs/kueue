//go:build !exclude_scheduler_library

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

package was

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// VirtualPodGroupName returns a collision-free name for a virtual pod group
// representing the workload.
func VirtualPodGroupName(wlName string) string {
	h := sha1.New()
	h.Write([]byte(wlName))
	hash := hex.EncodeToString(h.Sum(nil))[:hashLength]
	suffix := fmt.Sprintf("-%s", hash)
	prefix := fmt.Sprintf("virtual-pg-%s", wlName)
	maxPrefix := maxPodNameLength - len(suffix)
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], ".-")
	}

	return prefix + suffix
}

// BuildVirtualPodGroup creates one single gang PodGroup for the entire Workload.
func BuildVirtualPodGroup(wl *kueue.Workload) (*schedulingv1beta1.PodGroup, error) {
	if wl == nil {
		return nil, errors.New("workload must not be nil")
	}

	var totalPods int64
	for _, ps := range wl.Spec.PodSets {
		totalPods += int64(ps.Count)
	}
	if totalPods <= 0 {
		return nil, fmt.Errorf("workload %q has non-positive total pods count %d", wl.Name, totalPods)
	}
	if totalPods > math.MaxInt32 {
		return nil, fmt.Errorf("workload %q total pods count %d exceeds max int32", wl.Name, totalPods)
	}

	return &schedulingv1beta1.PodGroup{
		Name:      VirtualPodGroupName(wl.Name),
		Namespace: wl.Namespace,
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{
					MinCount: int32(totalPods),
				},
			},
		},
	}, nil
}
