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

package main

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestValidateFlags(t *testing.T) {
	tests := map[string]struct {
		cfg     Config
		wantErr error
	}{
		"valid config": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: 1, Containers: 1, Envs: 0},
			wantErr: nil,
		},
		"zero count": {
			cfg:     Config{Count: 0, QPS: 1.0, Workers: 1, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"zero qps": {
			cfg:     Config{Count: 1, QPS: 0, Workers: 1, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"zero workers": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: 0, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"zero containers": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: 1, Containers: 0, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"negative count": {
			cfg:     Config{Count: -1, QPS: 1.0, Workers: 1, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"negative qps": {
			cfg:     Config{Count: 1, QPS: -1.0, Workers: 1, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"negative workers": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: -1, Containers: 1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"negative containers": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: 1, Containers: -1, Envs: 0},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
		"negative envs": {
			cfg:     Config{Count: 1, QPS: 1.0, Workers: 1, Containers: 1, Envs: -1},
			wantErr: errors.New("--count, --qps, --workers, and --containers must be strictly positive (> 0). --envs must be >= 0"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotErr := validateFlags(&tc.cfg)
			if tc.wantErr != nil {
				if gotErr == nil {
					t.Errorf("validateFlags() returned nil error, want %v", tc.wantErr)
				} else if diff := cmp.Diff(tc.wantErr.Error(), gotErr.Error()); diff != "" {
					t.Errorf("validateFlags() error mismatch (-want +got):\n%s", diff)
				}
			} else if gotErr != nil {
				t.Errorf("validateFlags() unexpected error: %v", gotErr)
			}
		})
	}
}

func TestGeneratePadString(t *testing.T) {
	tests := map[string]struct {
		length  int
		wantLen int
	}{
		"zero length": {
			length:  0,
			wantLen: 0,
		},
		"small length": {
			length:  10,
			wantLen: 10,
		},
		"large length": {
			length:  100,
			wantLen: 100,
		},
		"negative length": {
			length:  -5,
			wantLen: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := generatePadString(tc.length)
			if len(got) != tc.wantLen {
				t.Errorf("generatePadString(%d) returned string of length %d, want %d", tc.length, len(got), tc.wantLen)
			}
		})
	}
}

func TestCreateWorkloadObj(t *testing.T) {
	tests := map[string]struct {
		idx        int
		containers int
		envs       int
		wantName   string
	}{
		"basic workload": {
			idx:        1,
			containers: 1,
			envs:       0,
			wantName:   "wl-1",
		},
		"heavy workload": {
			idx:        2,
			containers: 5,
			envs:       10,
			wantName:   "wl-2",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			obj := createWorkloadObj(tc.idx, tc.containers, tc.envs)
			if obj == nil {
				t.Fatalf("createWorkloadObj returned nil")
			}

			if gotName := obj.GetName(); gotName != tc.wantName {
				t.Errorf("GetName() = %v, want %v", gotName, tc.wantName)
			}
			if gotNamespace := obj.GetNamespace(); gotNamespace != "default" {
				t.Errorf("GetNamespace() = %v, want default", gotNamespace)
			}
			if gotKind := obj.GetKind(); gotKind != "Workload" {
				t.Errorf("GetKind() = %v, want Workload", gotKind)
			}
			if gotAPIVersion := obj.GetAPIVersion(); gotAPIVersion != "kueue.x-k8s.io/v1beta1" {
				t.Errorf("GetAPIVersion() = %v, want kueue.x-k8s.io/v1beta1", gotAPIVersion)
			}

			podSets, _, _ := unstructured.NestedSlice(obj.Object, "spec", "podSets")
			if len(podSets) != 1 {
				t.Fatalf("len(podSets) = %d, want 1", len(podSets))
			}

			containers, _, _ := unstructured.NestedSlice(podSets[0].(map[string]any), "template", "spec", "containers")
			if len(containers) != tc.containers {
				t.Errorf("len(containers) = %d, want %d", len(containers), tc.containers)
			}

			if tc.containers > 0 {
				env, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "env")
				if len(env) != tc.envs {
					t.Errorf("len(env) = %d, want %d", len(env), tc.envs)
				}
			}
		})
	}
}
