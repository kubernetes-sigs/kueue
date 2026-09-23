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

package mapping

import (
	"errors"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/kueue/pkg/resources"
)

var (
	ErrNoMapping = errors.New("no mapping found")
)

type Match struct {
	PriorityClassName string            `json:"priorityClassName"`
	Labels            map[string]string `json:"labels"`
	// Resources, if set, matches the Pods requesting a non-zero amount of all
	// the listed resources. An empty list matches any Pod.
	Resources []corev1.ResourceName `json:"resources"`
}

// Match checks the rule against a Pod's priority class name, labels and total
// resource requests. podRequests is provided by the caller so that it is
// computed only once per Pod instead of once per rule.
func (mm *Match) Match(priorityClassName string, labels map[string]string, podRequests resources.Requests) bool {
	if mm.PriorityClassName != "" && priorityClassName != mm.PriorityClassName {
		return false
	}
	for l, lv := range mm.Labels {
		if labels[l] != lv {
			return false
		}
	}
	for _, r := range mm.Resources {
		if podRequests.ResourceValue(r) <= 0 {
			return false
		}
	}
	return true
}

type Rule struct {
	Match        Match  `json:"match"`
	ToLocalQueue string `json:"toLocalQueue"`
	Skip         bool   `json:"skip"`
}

type Rules []Rule

func (mr Rules) QueueFor(p *corev1.Pod) (string, bool, bool) {
	podRequests := resources.NewRequestsFromPodSpec(&p.Spec)
	for i := range mr {
		if mr[i].Match.Match(p.Spec.PriorityClassName, p.Labels, podRequests) {
			return mr[i].ToLocalQueue, mr[i].Skip, true
		}
	}
	return "", false, false
}

func RulesFromFile(mappingFile string) (Rules, error) {
	ret := Rules{}
	yamlFile, err := os.ReadFile(mappingFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(yamlFile, &ret)
	if err != nil {
		return nil, fmt.Errorf("decoding %q: %w", mappingFile, err)
	}
	return ret, nil
}

func RulesForLabel(label string, m map[string]string) Rules {
	ret := make(Rules, 0, len(m))
	for labelValue, queue := range m {
		ret = append(ret, Rule{
			Match: Match{
				Labels: map[string]string{
					label: labelValue,
				},
			},
			ToLocalQueue: queue,
		})
	}
	return ret
}
