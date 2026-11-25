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
// When go.mod updates k8s.io/kubernetes to 1.38 (including #141932 and #142393
// on framework.Framework):
// 1. Delete the entire pkg/upstreamsync/preemption package.
// 2. Return framework.Framework directly from ProfileMap.FrameworkForPodGroup.
// 3. Call RunPodGroupPostFilterPlugins directly on framework.Framework.
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
	PreemptionManager() PreemptionManager
}

// PreemptionManagerFactory defines a factory function to create a PreemptionManager given a Handle.
//
// UPSTREAM-DIFF: copy of the 1.38 frameworkruntime.PreemptionManagerFactory, using this package's
// Handle and PreemptionManager instead of the fwk ones.
// Migration Plan (1.38): Delete this type and use frameworkruntime.PreemptionManagerFactory.
type PreemptionManagerFactory func(fh Handle) PreemptionManager

// PreemptionFramework wraps framework.Framework to provide Handle.PreemptionManager().
//
// Migration Plan (1.38): Delete PreemptionFramework once framework.Framework in k8s.io/kubernetes 1.38
// provides PreemptionManager().
type PreemptionFramework struct {
	framework.Framework
	pm PreemptionManager
}

// NewPreemptionFramework creates a new PreemptionFramework. If preemptionManagerFactory is not nil,
// it is called with the new PreemptionFramework to create its PreemptionManager.
func NewPreemptionFramework(fw framework.Framework, preemptionManagerFactory PreemptionManagerFactory) *PreemptionFramework {
	f := &PreemptionFramework{Framework: fw}
	if preemptionManagerFactory != nil {
		f.pm = preemptionManagerFactory(f)
	}
	return f
}

// PreemptionManager returns the PreemptionManager currently configured on the framework.
func (f *PreemptionFramework) PreemptionManager() PreemptionManager {
	return f.pm
}
