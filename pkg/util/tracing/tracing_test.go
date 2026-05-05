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

package tracing

import (
	"context"
	"testing"
	"time"
)

func TestNewNoOp(t *testing.T) {
	ctx := context.Background()
	n := NewNoOp()
	ctx, end := n.StartSpan(ctx, nil, "test", map[string]string{"k": "v"})
	if n.GetTraceContext(ctx) != "" {
		t.Fatalf("GetTraceContext: want empty string from noop")
	}
	if n.IsRecording(ctx) {
		t.Fatalf("IsRecording: want false from noop")
	}
	n.AddEvent(ctx, "e", map[string]string{"a": "b"})
	n.RecordCompletedPhaseSpan(ctx, "pending", time.Unix(1, 0), time.Unix(2, 0), map[string]string{"k": "v"})
	end()
}
