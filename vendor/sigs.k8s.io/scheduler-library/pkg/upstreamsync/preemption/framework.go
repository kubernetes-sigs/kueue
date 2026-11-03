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

// Package preemption is a temporary copy of the upstream PodGroup preemption logic
// from kubernetes/kubernetes#141932 and kubernetes/kubernetes#142393.
//
// UPSTREAM-DIFF: Because k8s.io/kubernetes v1.37.0 does not yet include PreemptionManager,
// ReprieveFilter, or Handle.PreemptionManager(), and defaultpreemption.DefaultPreemption
// has an unexported podGroupEvaluator field, this package copies PodGroupEvaluator and
// DefaultPreemption.PodGroupPostFilter and wraps framework.Framework in PreemptionFramework.
//
// Migration Plan:
// When go.mod updates k8s.io/kubernetes to 1.38 (including #141932, #142393, and
// #143016 on framework.Framework):
// 1. Delete the entire pkg/upstreamsync/preemption package.
// 2. Return framework.Framework directly from ProfileMap.FrameworkForPodGroup.
// 3. Call SetPreemptionManager and RunPodGroupPostFilterPlugins directly on framework.Framework.
package preemption

import (
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// Handle extends fwk.Handle with PreemptionManager() until k8s.io/kube-scheduler 1.38 adds it to fwk.Handle.
//
// Migration Plan (1.38): Delete this interface and use fwk.Handle directly.
type Handle interface {
	fwk.Handle
	// PreemptionManager returns the PreemptionManager for the current scheduling cycle.
	// Because simulation harnesses can replace the PreemptionManager between cycles via
	// SetPreemptionManager, plugins must call PreemptionManager() during PostFilter or
	// PodGroupPostFilter execution and must not cache the returned value across cycles.
	PreemptionManager() PreemptionManager
}

// PreemptionFramework wraps framework.Framework to provide Handle.PreemptionManager() and SetPreemptionManager().
//
// Migration Plan (1.38): Delete PreemptionFramework once framework.Framework in k8s.io/kubernetes 1.38
// provides PreemptionManager() and SetPreemptionManager().
type PreemptionFramework struct {
	framework.Framework
	pm PreemptionManager
}

// NewPreemptionFramework creates a new PreemptionFramework.
func NewPreemptionFramework(fw framework.Framework, preemptionManager PreemptionManager) *PreemptionFramework {
	return &PreemptionFramework{
		Framework: fw,
		pm:        preemptionManager,
	}
}

// PreemptionManager returns the PreemptionManager currently configured on the framework.
// PreemptionManager can be changed dynamically during scheduling cycles by usage of SetPreemptionManager(), so all plugins should
// use this method to get the current PreemptionManager.
func (f *PreemptionFramework) PreemptionManager() PreemptionManager {
	return f.pm
}

// SetPreemptionManager sets the PreemptionManager on the framework.
func (f *PreemptionFramework) SetPreemptionManager(pm PreemptionManager) {
	f.pm = pm
}
