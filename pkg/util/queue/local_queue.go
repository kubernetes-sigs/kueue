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

package queue

import (
	"fmt"
	"strings"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// LocalQueueReference is the full reference to LocalQueue formed as <namespace>/< kueue.LocalQueueName >.
type LocalQueueReference string

func NewLocalQueueReference(namespace string, name kueue.LocalQueueName) LocalQueueReference {
	return LocalQueueReference(namespace + "/" + string(name))
}

func ParseLocalQueueReference(ref LocalQueueReference) (string, kueue.LocalQueueName, error) {
	parts := strings.Split(string(ref), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid LocalQueueReference %s", ref)
	}
	return parts[0], kueue.LocalQueueName(parts[1]), nil
}

func MustParseLocalQueueReference(ref LocalQueueReference) (string, kueue.LocalQueueName) {
	namespace, name, err := ParseLocalQueueReference(ref)
	if err != nil {
		panic(err)
	}
	return namespace, name
}

// Key is the key used to index the queue.
func Key(q *kueue.LocalQueue) LocalQueueReference {
	return NewLocalQueueReference(q.Namespace, kueue.LocalQueueName(q.Name))
}

func KeyFromWorkload(w *kueue.Workload) LocalQueueReference {
	return NewLocalQueueReference(w.Namespace, w.Spec.QueueName)
}

func DefaultQueueKey(namespace string) LocalQueueReference {
	return NewLocalQueueReference(namespace, constants.DefaultLocalQueueName)
}
