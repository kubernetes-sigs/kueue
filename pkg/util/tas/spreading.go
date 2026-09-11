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

package tas

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	// defaultEnforcementMode is applied to a rule whose "enforcementMode" field
	// is omitted. Not API surface: the annotation is a JSON blob with no CRD
	// schema, so this default is applied at parse time rather than by the
	// apiserver.
	defaultEnforcementMode = kueue.TopologySpreadingEnforcementModeRequired

	minSpreadingRules = 1
	maxSpreadingRules = 2

	// shareScale is the fixed-point scale maxShareAllowingPlacement is reduced
	// to, so thresholds are evaluated in integer arithmetic. Milli is the
	// natural granularity of resource.Quantity, giving a resolution of 0.1%.
	shareScale = resource.Milli

	// shareScaleFactor is shareScale expressed as a multiplier: a share of 1
	// (a domain holding everything) is shareScaleFactor scaled units.
	shareScaleFactor = 1000
)

var (
	// ErrParseTopologySpreading indicates the annotation value is not valid
	// JSON, or its workloadLabelSelectors do not compile into a label selector.
	ErrParseTopologySpreading = errors.New("failed to parse topology spreading annotation")

	// ErrTopologySpreadingRuleCount indicates the parsed "rules" array is
	// empty or has more entries than currently supported.
	ErrTopologySpreadingRuleCount = errors.New("topology spreading rules must contain between 1 and 2 entries")

	// ErrTopologySpreadingSelectorMissing indicates "workloadLabelSelectors" is
	// absent or empty. An empty selector would match every Workload in the
	// namespace, so it is rejected rather than defaulted here.
	ErrTopologySpreadingSelectorMissing = errors.New("topology spreading workloadLabelSelectors must contain at least one requirement")

	// ErrTopologySpreadingSelectorInvalid indicates "workloadLabelSelectors"
	// parsed as JSON but does not describe a usable label selector - a
	// malformed label key or value, or an "In" requirement with no values.
	ErrTopologySpreadingSelectorInvalid = errors.New("topology spreading workloadLabelSelectors is not a valid label selector")
)

// SpreadingRule is the parsed form of one entry in the "rules" array of the
// kueue.x-k8s.io/podset-topology-spreading annotation.
type SpreadingRule struct {
	// TopologyKey is the topology level's node label key this rule applies to.
	TopologyKey string `json:"topologyKey"`

	// MaxShareAllowingPlacement is the maximum share, in the range (0, 1)
	// exclusive, of matching Workloads a domain at this level may already hold
	// for the next PodSet group to still be placed there.
	MaxShareAllowingPlacement resource.Quantity `json:"maxShareAllowingPlacement"`

	// EnforcementMode is either Required (the default) or Preferred.
	EnforcementMode kueue.TopologySpreadingEnforcementMode `json:"enforcementMode,omitempty"`
}

// ExceedsShare reports whether a domain already holding count out of total is
// over this rule's maxShareAllowingPlacement, and so may not receive the next
// PodSet group. Whether being over the share bans the domain or merely
// deprioritizes it is the caller's decision, per EnforcementMode.
//
// The comparison is cross-multiplied against the share reduced to shareScale,
// so it stays in integer arithmetic and never rounds a float. total == 0 (the
// cold-start case, nothing admitted yet) is never over the share.
func (r *SpreadingRule) ExceedsShare(count, total int32) bool {
	maxShareScaled := r.MaxShareAllowingPlacement.ScaledValue(shareScale)
	return int64(count)*shareScaleFactor > maxShareScaled*int64(total)
}

// SpreadingSpec is the parsed form of the
// kueue.x-k8s.io/podset-topology-spreading annotation: exactly the JSON the
// user wrote, with defaults applied. Every field maps to a path the user can
// act on, which is what lets the webhook report errors against
// workloadLabelSelectors[i].operator and the like. Its fields are exported
// because encoding/json requires that to unmarshal into them.
type SpreadingSpec struct {
	// WorkloadLabelSelectors is the list of label selector requirements
	// selecting, among Workloads in the same namespace, which ones count
	// towards the rules below. Read Selector to match against it.
	WorkloadLabelSelectors []metav1.LabelSelectorRequirement `json:"workloadLabelSelectors,omitempty"`

	// Rules is the list of per-topology-level spreading constraints.
	Rules []SpreadingRule `json:"rules"`

	// selector is WorkloadLabelSelectors compiled, so matching Workloads never
	// recompiles it. Unexported, and set by every constructor, so a
	// SpreadingSpec obtained from this package always has one - there is no
	// half-built state for callers to trip over.
	selector labels.Selector
}

// Selector matches, among Workloads in the same namespace, those counting
// towards Rules. Cheap: the selector was compiled when the spec was built.
func (s *SpreadingSpec) Selector() labels.Selector {
	return s.selector
}

// NewSpreadingSpec builds a spec from already-decoded parts, compiling the
// label selector. ParseSpreadingAnnotation is the usual way in; this exists
// for callers holding the parts directly, such as tests in other packages
// that cannot reach the unexported selector.
func NewSpreadingSpec(selectors []metav1.LabelSelectorRequirement, rules []SpreadingRule) (*SpreadingSpec, error) {
	spec := &SpreadingSpec{WorkloadLabelSelectors: selectors, Rules: rules}
	if err := spec.compileSelector(); err != nil {
		return nil, err
	}
	return spec, nil
}

func (s *SpreadingSpec) compileSelector() error {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchExpressions: s.WorkloadLabelSelectors,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTopologySpreadingSelectorInvalid, err)
	}
	s.selector = selector
	return nil
}

// ParseSpreadingAnnotation parses the value of the
// kueue.x-k8s.io/podset-topology-spreading annotation, returning a spec whose
// Selector is ready to match.
//
// It only checks what would make the spec entirely unusable: invalid JSON, an
// out-of-range rule count, and workloadLabelSelectors that are missing or do
// not compile. Per-field, field.Path-scoped checks (bad topology keys,
// out-of-range shares, unknown enforcement modes, duplicate keys, alpha
// restrictions on the selector) are the webhook's responsibility and are
// re-validated there.
func ParseSpreadingAnnotation(value string) (*SpreadingSpec, error) {
	var spec SpreadingSpec
	if err := json.Unmarshal([]byte(value), &spec); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseTopologySpreading, err)
	}

	if len(spec.Rules) < minSpreadingRules || len(spec.Rules) > maxSpreadingRules {
		return nil, fmt.Errorf("%w: got %d", ErrTopologySpreadingRuleCount, len(spec.Rules))
	}

	for i := range spec.Rules {
		if spec.Rules[i].EnforcementMode == "" {
			spec.Rules[i].EnforcementMode = defaultEnforcementMode
		}
	}

	if len(spec.WorkloadLabelSelectors) == 0 {
		return nil, ErrTopologySpreadingSelectorMissing
	}
	if err := spec.compileSelector(); err != nil {
		return nil, err
	}

	return &spec, nil
}
