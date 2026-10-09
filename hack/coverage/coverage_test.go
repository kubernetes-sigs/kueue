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

package coverage_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"sigs.k8s.io/kueue/hack/coverage"
)

func TestParse(t *testing.T) {
	product := "sigs.k8s.io/kueue/pkg/util/slices/slices.go:10.2,12.16 2 1"
	tests := map[string]struct {
		input   string
		want    coverage.Profile
		wantErr error
	}{
		"product block": {
			input: "mode: set\n" + product + "\n",
			want: coverage.Profile{
				Mode: "set",
				Blocks: []coverage.Block{{
					File:      "sigs.k8s.io/kueue/pkg/util/slices/slices.go",
					StartLine: 10,
					StartCol:  2,
					EndLine:   12,
					EndCol:    16,
					NumStmt:   2,
					Count:     1,
				}},
			},
		},
		"count mode uncovered": {
			input: "mode: count\nsigs.k8s.io/kueue/pkg/scheduler/scheduler.go:1.1,2.2 1 0\n",
			want: coverage.Profile{
				Mode: "count",
				Blocks: []coverage.Block{{
					File:      "sigs.k8s.io/kueue/pkg/scheduler/scheduler.go",
					StartLine: 1,
					StartCol:  1,
					EndLine:   2,
					EndCol:    2,
					NumStmt:   1,
					Count:     0,
				}},
			},
		},
		"mode only": {
			input: "mode: atomic\n",
			want:  coverage.Profile{Mode: "atomic"},
		},
		"empty": {
			input:   "",
			wantErr: coverage.ErrEmptyProfile,
		},
		"whitespace": {
			input:   " \n\t\n",
			wantErr: coverage.ErrEmptyProfile,
		},
		"missing mode": {
			input:   product + "\n",
			wantErr: coverage.ErrCorruptProfile,
		},
		"bad mode": {
			input:   "mode: bogus\n",
			wantErr: coverage.ErrCorruptProfile,
		},
		"bad block": {
			input:   "mode: set\nnot-a-block\n",
			wantErr: coverage.ErrCorruptProfile,
		},
		"truncated block": {
			input:   "mode: set\nsigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1\n",
			wantErr: coverage.ErrCorruptProfile,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := coverage.Parse(strings.NewReader(tc.input))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.input, got.Format()); diff != "" {
				t.Errorf("Format() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	const product = "sigs.k8s.io/kueue/pkg/util/slices/slices.go"
	tests := map[string]struct {
		path string
		keep bool
	}{
		"product package":    {path: product, keep: true},
		"another pkg file":   {path: "sigs.k8s.io/kueue/pkg/scheduler/flavor.go", keep: true},
		"vendor filename":    {path: "sigs.k8s.io/kueue/pkg/vendor.go", keep: true},
		"client-go filename": {path: "sigs.k8s.io/kueue/pkg/client-go.go", keep: true},
		"module vendor":      {path: "sigs.k8s.io/kueue/vendor/example.com/lib/lib.go", keep: false},
		"relative vendor":    {path: "vendor/example.com/lib/lib.go", keep: false},
		"deepcopy":           {path: "sigs.k8s.io/kueue/apis/kueue/v1beta2/zz_generated.deepcopy.go", keep: false},
		"conversion":         {path: "sigs.k8s.io/kueue/apis/visibility/v1beta2/zz_generated.conversion.go", keep: false},
		"openapi":            {path: "sigs.k8s.io/kueue/apis/visibility/openapi/zz_generated.openapi.go", keep: false},
		"protobuf":           {path: "sigs.k8s.io/kueue/pkg/foo/bar.pb.go", keep: false},
		"grpc protobuf":      {path: "sigs.k8s.io/kueue/pkg/foo/bar_grpc.pb.go", keep: false},
		"module client-go":   {path: "sigs.k8s.io/kueue/client-go/clientset/versioned/clientset.go", keep: false},
		"relative client-go": {path: "client-go/applyconfiguration/kueue/v1beta2/cohort.go", keep: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			in := coverage.Profile{
				Mode: "set",
				Blocks: []coverage.Block{{
					File: tc.path, StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 1,
				}},
			}
			got := coverage.Filter(in)
			if tc.keep {
				if diff := cmp.Diff(in.Blocks, got.Blocks); diff != "" {
					t.Errorf("Filter() mismatch (-want +got):\n%s", diff)
				}
				return
			}
			if len(got.Blocks) != 0 {
				t.Fatalf("Filter() kept %s", tc.path)
			}
			if got.Mode != "set" {
				t.Fatalf("Filter() mode = %q, want set", got.Mode)
			}
		})
	}

	t.Run("mixed profile keeps product blocks in order", func(t *testing.T) {
		in := coverage.Profile{
			Mode: "set",
			Blocks: []coverage.Block{
				{File: product, StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 1},
				{File: "sigs.k8s.io/kueue/apis/kueue/v1beta2/zz_generated.deepcopy.go", StartLine: 3, StartCol: 1, EndLine: 4, EndCol: 2, NumStmt: 4, Count: 1},
				{File: "sigs.k8s.io/kueue/pkg/scheduler/scheduler.go", StartLine: 5, StartCol: 1, EndLine: 6, EndCol: 2, NumStmt: 2, Count: 0},
				{File: "vendor/k8s.io/api/core/v1/types.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2, NumStmt: 1, Count: 1},
			},
		}
		got := coverage.Filter(in)
		want := []coverage.Block{in.Blocks[0], in.Blocks[2]}
		if diff := cmp.Diff(want, got.Blocks); diff != "" {
			t.Errorf("Filter() mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestSummary(t *testing.T) {
	tests := map[string]struct {
		profile coverage.Profile
		want    string
	}{
		"partial": {
			profile: coverage.Profile{
				Mode: "set",
				Blocks: []coverage.Block{
					{File: "b.go", NumStmt: 1, Count: 0},
					{File: "a.go", NumStmt: 3, Count: 1},
					{File: "a.go", NumStmt: 1, Count: 0},
				},
			},
			want: "" +
				"a.go: 75.0% of statements (3/4)\n" +
				"b.go: 0.0% of statements (0/1)\n" +
				"total: (statements) 60.0%\n",
		},
		"count above one is covered": {
			profile: coverage.Profile{
				Mode:   "count",
				Blocks: []coverage.Block{{File: "a.go", NumStmt: 2, Count: 5}},
			},
			want: "" +
				"a.go: 100.0% of statements (2/2)\n" +
				"total: (statements) 100.0%\n",
		},
		"no statements": {
			profile: coverage.Profile{Mode: "set"},
			want:    "total: (statements) 0.0% (no statements)\n",
		},
		"uncovered product code": {
			profile: coverage.Profile{
				Mode:   "set",
				Blocks: []coverage.Block{{File: "pkg/util/slices/slices.go", NumStmt: 4, Count: 0}},
			},
			want: "" +
				"pkg/util/slices/slices.go: 0.0% of statements (0/4)\n" +
				"total: (statements) 0.0%\n",
		},
		"duplicate set blocks merge to covered": {
			profile: coverage.Profile{
				Mode: "set",
				Blocks: []coverage.Block{
					{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 1},
					{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 0},
				},
			},
			want: "" +
				"a.go: 100.0% of statements (1/1)\n" +
				"total: (statements) 100.0%\n",
		},
		"duplicate count blocks sum and cover once": {
			profile: coverage.Profile{
				Mode: "count",
				Blocks: []coverage.Block{
					{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 2},
					{File: "a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 2, NumStmt: 1, Count: 3},
				},
			},
			want: "" +
				"a.go: 100.0% of statements (1/1)\n" +
				"total: (statements) 100.0%\n",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := coverage.Summary(tc.profile)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Summary() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOutputPaths(t *testing.T) {
	filtered, summary := coverage.OutputPaths("artifacts/cover-shard-0.out")
	if filtered != "artifacts/cover-shard-0.filtered.out" || summary != "artifacts/cover-shard-0.filtered.txt" {
		t.Fatalf("OutputPaths() = %q, %q", filtered, summary)
	}
	filtered, summary = coverage.OutputPaths("artifacts/cover.out")
	if filtered != "artifacts/cover.filtered.out" || summary != "artifacts/cover.filtered.txt" {
		t.Fatalf("OutputPaths() = %q, %q", filtered, summary)
	}
}

func TestReport(t *testing.T) {
	product := "mode: set\n" +
		"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 2 1\n" +
		"sigs.k8s.io/kueue/pkg/util/slices/slices.go:3.1,4.2 2 0\n" +
		"sigs.k8s.io/kueue/apis/kueue/v1beta2/zz_generated.deepcopy.go:1.1,2.2 10 1\n" +
		"sigs.k8s.io/kueue/client-go/clientset/versioned/clientset.go:1.1,2.2 8 1\n" +
		"vendor/example.com/lib/lib.go:1.1,2.2 3 1\n" +
		"sigs.k8s.io/kueue/pkg/foo/bar.pb.go:1.1,2.2 5 1\n"

	t.Run("writes filtered profile and percentage", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "cover-shard-0.out")
		if err := os.WriteFile(profile, []byte(product), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		if err := coverage.Report(profile, &stdout); err != nil {
			t.Fatal(err)
		}
		filteredPath := filepath.Join(dir, "cover-shard-0.filtered.out")
		summaryPath := filepath.Join(dir, "cover-shard-0.filtered.txt")
		filtered, err := os.ReadFile(filteredPath)
		if err != nil {
			t.Fatal(err)
		}
		wantProfile := "mode: set\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 2 1\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:3.1,4.2 2 0\n"
		if diff := cmp.Diff(wantProfile, string(filtered)); diff != "" {
			t.Errorf("filtered profile mismatch (-want +got):\n%s", diff)
		}
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			t.Fatal(err)
		}
		wantSummary := "sigs.k8s.io/kueue/pkg/util/slices/slices.go: 50.0% of statements (2/4)\n" +
			"total: (statements) 50.0%\n"
		if diff := cmp.Diff(wantSummary, string(summary)); diff != "" {
			t.Errorf("summary file mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantSummary, stdout.String()); diff != "" {
			t.Errorf("stdout mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("empty after filtering exits clean", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "cover.out")
		input := "mode: set\nvendor/example.com/lib/lib.go:1.1,2.2 3 1\n"
		if err := os.WriteFile(profile, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		if err := coverage.Report(profile, &stdout); err != nil {
			t.Fatal(err)
		}
		filtered, err := os.ReadFile(filepath.Join(dir, "cover.filtered.out"))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("mode: set\n", string(filtered)); diff != "" {
			t.Errorf("filtered profile mismatch (-want +got):\n%s", diff)
		}
		want := "total: (statements) 0.0% (no statements)\n"
		summary, err := os.ReadFile(filepath.Join(dir, "cover.filtered.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(summary) != want || stdout.String() != want {
			t.Fatalf("summary = %q, stdout = %q, want %q", summary, stdout.String(), want)
		}
	})

	t.Run("merges duplicate blocks into the filtered profile", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "cover.out")
		input := "mode: count\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 2\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 3\n"
		if err := os.WriteFile(profile, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		if err := coverage.Report(profile, &stdout); err != nil {
			t.Fatal(err)
		}
		filtered, err := os.ReadFile(filepath.Join(dir, "cover.filtered.out"))
		if err != nil {
			t.Fatal(err)
		}
		wantProfile := "mode: count\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 5\n"
		if diff := cmp.Diff(wantProfile, string(filtered)); diff != "" {
			t.Errorf("filtered profile mismatch (-want +got):\n%s", diff)
		}
		wantSummary := "sigs.k8s.io/kueue/pkg/util/slices/slices.go: 100.0% of statements (1/1)\n" +
			"total: (statements) 100.0%\n"
		summary, err := os.ReadFile(filepath.Join(dir, "cover.filtered.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(wantSummary, string(summary)); diff != "" {
			t.Errorf("summary file mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantSummary, stdout.String()); diff != "" {
			t.Errorf("stdout mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("set mode duplicate blocks stay covered once", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "cover.out")
		input := "mode: set\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 1\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 1\n"
		if err := os.WriteFile(profile, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := coverage.Report(profile, io.Discard); err != nil {
			t.Fatal(err)
		}
		filtered, err := os.ReadFile(filepath.Join(dir, "cover.filtered.out"))
		if err != nil {
			t.Fatal(err)
		}
		wantProfile := "mode: set\n" +
			"sigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 1 1\n"
		if diff := cmp.Diff(wantProfile, string(filtered)); diff != "" {
			t.Errorf("filtered profile mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("zero coverage does not fail", func(t *testing.T) {
		dir := t.TempDir()
		profile := filepath.Join(dir, "cover.out")
		input := "mode: set\nsigs.k8s.io/kueue/pkg/util/slices/slices.go:1.1,2.2 4 0\n"
		if err := os.WriteFile(profile, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := coverage.Report(profile, io.Discard); err != nil {
			t.Fatal(err)
		}
	})

	errTests := map[string]struct {
		path    string
		body    *string
		wantErr error
	}{
		"missing": {
			wantErr: os.ErrNotExist,
		},
		"empty file": {
			body:    new(""),
			wantErr: coverage.ErrEmptyProfile,
		},
		"corrupt": {
			body:    new("this is not a profile\n"),
			wantErr: coverage.ErrCorruptProfile,
		},
	}
	for name, tc := range errTests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "cover.out")
			if tc.body != nil {
				if err := os.WriteFile(profile, []byte(*tc.body), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				profile = filepath.Join(dir, "missing.out")
			}
			err := coverage.Report(profile, io.Discard)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Report() error = %v, want %v", err, tc.wantErr)
			}
			if _, statErr := os.Stat(strings.TrimSuffix(profile, ".out") + ".filtered.out"); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("filtered profile stat error = %v, want not exist", statErr)
			}
		})
	}

	t.Run("empty path", func(t *testing.T) {
		err := coverage.Report("", io.Discard)
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Report() error = %v, want empty path", err)
		}
	})
}
