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

	"k8s.io/apimachinery/pkg/labels"
)

const (
	// PodSetTopologySpreadingAnnotation contains a JSON-encoded object describing
	// how Workloads matching a label selector should be spread across topology
	// domains. The value carries a workload label selector and a list of rules,
	// each naming a topology level key and the maximum percentage of matching
	// Workloads that may be placed within a single domain at that level.
	//
	// This annotation must be set alongside one of PodSetRequiredTopologyAnnotation,
	// PodSetPreferredTopologyAnnotation or PodSetUnconstrainedTopologyAnnotation -
	// otherwise the Workload never engages Topology Aware Scheduling and spreading
	// never takes effect.
	//
	// This annotation is alpha-level for the TASTopologySpreading feature gate.
	PodSetTopologySpreadingAnnotation = "kueue.x-k8s.io/topology-spreading"
)

// TopologySpreadingRuleType determines whether a topology-spreading rule
// constrains placement or only influences its ordering. It is the type of the
// "type" field of a rule in PodSetTopologySpreadingAnnotation.
type TopologySpreadingRuleType string

const (
	// TopologySpreadingRuleRequired indicates that a spreading rule must be
	// respected; domains that would exceed maxDomainPercentage are excluded
	// from placement.
	TopologySpreadingRuleRequired TopologySpreadingRuleType = "Required"

	// TopologySpreadingRulePreferred indicates that a spreading rule is a
	// preference; domains that would exceed maxDomainPercentage are penalized
	// in placement ordering but not excluded.
	TopologySpreadingRulePreferred TopologySpreadingRuleType = "Preferred"
)

const (
	// defaultSpreadingRuleType is applied to a rule whose "type" field is
	// omitted. Not API surface: the annotation is a JSON blob with no CRD
	// schema, so this default is applied at parse time rather than by the
	// apiserver.
	defaultSpreadingRuleType = TopologySpreadingRuleRequired

	minSpreadingRules = 1
	maxSpreadingRules = 2
)

var (
	// ErrParseTopologySpreading indicates the annotation value is not valid
	// JSON, or its workloadLabelSelector does not parse as a label selector.
	ErrParseTopologySpreading = errors.New("failed to parse topology spreading annotation")

	// ErrTopologySpreadingRuleCount indicates the parsed "rules" array is
	// empty or has more entries than currently supported.
	ErrTopologySpreadingRuleCount = errors.New("topology spreading rules must contain between 1 and 2 entries")
)

// SpreadingRule is the parsed form of one entry in the "rules" array of the
// kueue.x-k8s.io/topology-spreading annotation.
type SpreadingRule struct {
	// Key is the topology level's node label key this rule applies to.
	Key string `json:"key"`

	// MaxDomainPercentage is the maximum percentage (1-99) of matching
	// Workloads that may be placed within a single domain at this level.
	MaxDomainPercentage int32 `json:"maxDomainPercentage"`

	// Type is either Required (the default) or Preferred.
	Type TopologySpreadingRuleType `json:"type,omitempty"`
}

// SpreadingSpec is the parsed form of the kueue.x-k8s.io/topology-spreading
// annotation. Exported because workload.Info.TopologySpreading holds it across
// a package boundary; its fields are exported because encoding/json requires
// that to unmarshal into them.
type SpreadingSpec struct {
	// WorkloadLabelSelectorStr is the raw wire-format value of the
	// workloadLabelSelector field, selecting, among Workloads in the same
	// namespace, which ones count towards the rules below. Callers should use
	// the compiled WorkloadLabelSelector instead.
	WorkloadLabelSelectorStr string `json:"workloadLabelSelector"`

	// Rules is the list of per-topology-level spreading constraints.
	Rules []SpreadingRule `json:"rules"`

	// WorkloadLabelSelector is the compiled form of WorkloadLabelSelectorStr,
	// computed once at parse time so callers never re-parse it. No JSON tag:
	// it is derived, not part of the wire format.
	WorkloadLabelSelector labels.Selector `json:"-"`
}

// ParseSpreadingAnnotation parses the value of the
// kueue.x-k8s.io/topology-spreading annotation.
//
// It only checks what would make the spec entirely unusable: invalid JSON, an
// out-of-range rule count, and an unparseable workloadLabelSelector.
// Per-field, field.Path-scoped checks (bad topology keys, out-of-range
// percentages, unknown rule types, duplicate keys) are the webhook's
// responsibility and are re-validated there.
func ParseSpreadingAnnotation(value string) (*SpreadingSpec, error) {
	var spec SpreadingSpec
	if err := json.Unmarshal([]byte(value), &spec); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseTopologySpreading, err)
	}

	if len(spec.Rules) < minSpreadingRules || len(spec.Rules) > maxSpreadingRules {
		return nil, fmt.Errorf("%w: got %d", ErrTopologySpreadingRuleCount, len(spec.Rules))
	}

	for i, rule := range spec.Rules {
		if rule.Type == "" {
			spec.Rules[i].Type = defaultSpreadingRuleType
		}
	}

	selector, err := labels.Parse(spec.WorkloadLabelSelectorStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid workloadLabelSelector: %w", ErrParseTopologySpreading, err)
	}
	spec.WorkloadLabelSelector = selector

	return &spec, nil
}
