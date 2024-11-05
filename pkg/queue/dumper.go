/*
Copyright 2023 The Kubernetes Authors.

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

package queue

import (
	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

// LogDump dumps the pending and inadmissible workloads for each ClusterQueue into the log,
// one line per ClusterQueue.
func (m *Manager) LogDump(log logr.Logger) {
	m.Lock()
	defer m.Unlock()
	for name, cq := range m.hm.ClusterQueues {
		pending, _ := cq.Dump()
		inadmissible, _ := cq.DumpInadmissible()
		log.Info("Found pending and inadmissible workloads in ClusterQueue",
			"clusterQueue", klog.KRef("", name),
			"pending", pending,
			"inadmissible", inadmissible)
	}
}

// Dump is a dump of the queues and it's elements (unordered).
// Only use for testing purposes.
func (m *Manager) Dump() map[string][]string {
	m.Lock()
	defer m.Unlock()
	if len(m.hm.ClusterQueues) == 0 {
		return nil
	}
	dump := make(map[string][]string, len(m.hm.ClusterQueues))
	for key, cq := range m.hm.ClusterQueues {
		if elements, ok := cq.Dump(); ok {
			dump[key] = elements
		}
	}
	if len(dump) == 0 {
		return nil
	}
	return dump
}

// DumpInadmissible is a dump of the inadmissible workloads list.
// Only use for testing purposes.
func (m *Manager) DumpInadmissible() map[string][]string {
	m.Lock()
	defer m.Unlock()
	if len(m.hm.ClusterQueues) == 0 {
		return nil
	}
	dump := make(map[string][]string, len(m.hm.ClusterQueues))
	for key, cq := range m.hm.ClusterQueues {
		if elements, ok := cq.DumpInadmissible(); ok {
			dump[key] = elements
		}
	}
	if len(dump) == 0 {
		return nil
	}
	return dump
}
