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

package dra

import (
	"testing"
)

func TestExtendedResourceCache(t *testing.T) {
	cache := NewExtendedResourceCache()

	t.Run("empty cache", func(t *testing.T) {
		if cache.Has("nvidia.com/gpu") {
			t.Error("expected false when cache is empty")
		}
	})

	t.Run("add single DeviceClass", func(t *testing.T) {
		cache.Add("nvidia.com/gpu", "gpu.nvidia.com")
		if !cache.Has("nvidia.com/gpu") {
			t.Error("expected true for nvidia.com/gpu")
		}
		if cache.Has("google.com/tpu") {
			t.Error("expected false for google.com/tpu")
		}
	})

	t.Run("add multiple DeviceClasses", func(t *testing.T) {
		cache.Add("google.com/tpu", "tpu.google.com")
		if !cache.Has("nvidia.com/gpu") {
			t.Error("expected true for nvidia.com/gpu")
		}
		if !cache.Has("google.com/tpu") {
			t.Error("expected true for google.com/tpu")
		}
	})

	t.Run("remove DeviceClass", func(t *testing.T) {
		cache.Remove("nvidia.com/gpu", "gpu.nvidia.com")
		if cache.Has("nvidia.com/gpu") {
			t.Error("expected false after removing gpu DeviceClass")
		}
		if !cache.Has("google.com/tpu") {
			t.Error("expected true for tpu that still exists")
		}
	})

	t.Run("multiple DeviceClasses backing same resource", func(t *testing.T) {
		cache.Add("nvidia.com/gpu", "gpu-a.nvidia.com")
		cache.Add("nvidia.com/gpu", "gpu-b.nvidia.com")
		if !cache.Has("nvidia.com/gpu") {
			t.Error("expected true with two DeviceClasses backing gpu")
		}
		cache.Remove("nvidia.com/gpu", "gpu-a.nvidia.com")
		if !cache.Has("nvidia.com/gpu") {
			t.Error("expected true with one DeviceClass still backing gpu")
		}
		cache.Remove("nvidia.com/gpu", "gpu-b.nvidia.com")
		if cache.Has("nvidia.com/gpu") {
			t.Error("expected false after all DeviceClasses removed")
		}
	})

	t.Run("remove non-existent DeviceClass is no-op", func(t *testing.T) {
		cache.Remove("nonexistent.com/resource", "nonexistent.example.com")
		if cache.Has("nonexistent.com/resource") {
			t.Error("expected false for non-existent resource")
		}
	})
}
