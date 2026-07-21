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

package resources

import (
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestResourceFormatterIsolation(t *testing.T) {
	configured := NewResourceFormatter()
	configured.RegisterBinaryFormattedResource("gpu.memory")
	configuredQuantity := configured.ResourceQuantity("gpu.memory", 9984*1024*1024)
	if got := configuredQuantity.String(); got != "9984Mi" {
		t.Errorf("configured formatter returned %q, want 9984Mi", got)
	}
	otherQuantity := NewResourceFormatter().ResourceQuantity("gpu.memory", 9984*1024*1024)
	if got := otherQuantity.String(); got != "10468982784" {
		t.Errorf("unconfigured formatter returned %q, want 10468982784", got)
	}
}

func TestResourceFormatterConcurrentRegistrationAndFormatting(t *testing.T) {
	formatter := NewResourceFormatter()
	name := corev1.ResourceName("example.com/gpu-memory")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			<-start
			for range 1000 {
				formatter.RegisterBinaryFormattedResource(name)
			}
		})
	}
	for range 8 {
		wg.Go(func() {
			<-start
			for range 1000 {
				_ = formatter.ResourceQuantity(name, 9984*1024*1024)
			}
		})
	}
	close(start)
	wg.Wait()
	quantity := formatter.ResourceQuantity(name, 9984*1024*1024)
	if got := quantity.String(); got != "9984Mi" {
		t.Errorf("registered resource formatted as %q, want 9984Mi", got)
	}
}

func TestNilResourceFormatterUsesDefaultFormatting(t *testing.T) {
	var formatter *ResourceFormatter
	quantity := formatter.ResourceQuantity("gpu.memory", 9984*1024*1024)
	if got := quantity.String(); got != "10468982784" {
		t.Errorf("nil formatter returned %q, want 10468982784", got)
	}
}
