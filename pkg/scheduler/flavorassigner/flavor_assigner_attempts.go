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

package flavorassigner

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
)

// FlavorAssignmentAttempt captures one attempted flavor and its worst-case outcome
// across the requested resources.
type FlavorAssignmentAttempt struct {
	Flavor                kueue.ResourceFlavorReference
	Mode                  FlavorAssignmentMode
	Borrow                int
	PreemptionPossibility *preemptioncommon.PreemptionPossibility
	Reasons               []string
}

type FlavorAssignmentAttempts []FlavorAssignmentAttempt

func newFlavorAssignmentAttempts(len int) FlavorAssignmentAttempts {
	fa := make(FlavorAssignmentAttempts, 0, len)
	return fa
}

func (fa *FlavorAssignmentAttempts) AddNoFitFlavorAttempt(flavor kueue.ResourceFlavorReference, status *Status) {
	flavorAttempt := FlavorAssignmentAttempt{
		Flavor: flavor,
		Mode:   NoFit,
	}
	if status != nil {
		flavorAttempt.Reasons = append(flavorAttempt.Reasons, status.reasons...)
	}
	*fa = append(*fa, flavorAttempt)
}

func (fa *FlavorAssignmentAttempts) AddRepresentativeModeFlavorAttempt(
	flavor kueue.ResourceFlavorReference,
	preemptionMode preemptionMode,
	maxBorrow int, reasons []string,
) {
	flavorAssignmentMode := preemptionMode.flavorAssignmentMode()
	flavorAttempt := FlavorAssignmentAttempt{
		Flavor:                flavor,
		Mode:                  flavorAssignmentMode,
		PreemptionPossibility: preemptionMode.preemptionPossibility(),
		Borrow:                maxBorrow,
	}
	if len(reasons) > 0 {
		slices.Sort(reasons)
		flavorAttempt.Reasons = append(flavorAttempt.Reasons, reasons...)
	}
	*fa = append(*fa, flavorAttempt)
}

func mergeFlavorAttemptsForResource(
	dst map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt,
	src FlavorAssignmentAttempts,
	resName corev1.ResourceName,
	cq *schdcache.ClusterQueueSnapshot,
) {
	mergeFlavorAttempts(dst, src)

	if len(src) > 0 {
		rg := cq.RGByResource(resName)
		rgFlavorSet := sets.New(rg.Flavors...)

		present := make(map[kueue.ResourceFlavorReference]struct{}, len(src))
		for _, c := range src {
			present[c.Flavor] = struct{}{}
		}

		for flv, at := range dst {
			if !rgFlavorSet.Has(flv) {
				continue
			}
			if _, ok := present[flv]; !ok {
				msg := fmt.Sprintf("flavor %s does not provide resource %s", flv, resName)
				if at.Mode != NoFit {
					at.Mode = NoFit
				}
				at.Reasons = mergeUnique(at.Reasons, []string{msg})
				slices.Sort(at.Reasons)
				dst[flv] = at
			}
		}
	}
}

func mergeFlavorAttempts(dst map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt, src FlavorAssignmentAttempts) {
	for _, at := range src {
		if existing, ok := dst[at.Flavor]; ok {
			worst := existing.Mode
			worst = min(at.Mode, worst)

			maxBorrow := existing.Borrow
			maxBorrow = max(at.Borrow, maxBorrow)

			existingPreemption := existing.PreemptionPossibility
			if at.PreemptionPossibility != nil && (existingPreemption == nil || *at.PreemptionPossibility < *existingPreemption) {
				existingPreemption = at.PreemptionPossibility
			}

			reasons := mergeUnique(existing.Reasons, at.Reasons)
			slices.Sort(reasons)
			dst[at.Flavor] = FlavorAssignmentAttempt{
				Flavor:                at.Flavor,
				Mode:                  worst,
				Borrow:                maxBorrow,
				PreemptionPossibility: existingPreemption,
				Reasons:               reasons,
			}
			continue
		}

		cp := FlavorAssignmentAttempt{
			Flavor:                at.Flavor,
			Mode:                  at.Mode,
			Borrow:                at.Borrow,
			PreemptionPossibility: at.PreemptionPossibility,
			Reasons:               mergeUnique(nil, at.Reasons),
		}
		slices.Sort(cp.Reasons)
		dst[at.Flavor] = cp
	}
}

func finalizeFlavorAssignmentAttempts(consideredFlavors map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt) []FlavorAssignmentAttempt {
	finalConsidered := make([]FlavorAssignmentAttempt, 0, len(consideredFlavors))
	for _, v := range consideredFlavors {
		finalConsidered = append(finalConsidered, v)
	}
	slices.SortFunc(finalConsidered, func(a, b FlavorAssignmentAttempt) int {
		return strings.Compare(string(a.Flavor), string(b.Flavor))
	})
	return finalConsidered
}

func mergeUnique(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

func FormatFlavorAssignmentAttemptsForEvents(a Assignment) string {
	if len(a.PodSets) == 0 {
		return ""
	}
	var b strings.Builder
	for _, ps := range a.PodSets {
		if len(ps.FlavorAssignmentAttempts) == 0 {
			continue
		}
		var attemptsBuilder strings.Builder
		for _, att := range ps.FlavorAssignmentAttempts {
			if att.Mode == Fit && att.Borrow == 0 {
				continue
			}
			if attemptsBuilder.Len() > 0 {
				attemptsBuilder.WriteString(", ")
			}
			attemptsBuilder.WriteString(string(att.Flavor))
			attemptsBuilder.WriteString("(")
			if att.PreemptionPossibility == nil {
				attemptsBuilder.WriteString(att.Mode.String())
			} else {
				fmt.Fprintf(&attemptsBuilder, "preemptionMode=%s", att.PreemptionPossibility)
			}
			if att.Borrow > 0 {
				fmt.Fprintf(&attemptsBuilder, ";borrow=%d", att.Borrow)
			}
			if len(att.Reasons) > 0 {
				attemptsBuilder.WriteString(";")
				attemptsBuilder.WriteString(strings.Join(att.Reasons, ";"))
			}
			attemptsBuilder.WriteString(")")
		}
		if attemptsBuilder.Len() == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(string(ps.Name))
		b.WriteString(": ")
		b.WriteString(attemptsBuilder.String())
	}
	return b.String()
}
