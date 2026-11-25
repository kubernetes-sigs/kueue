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

package generators

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

var cacheDirDefault = regexp.MustCompile(`--cache-dir string(?:&nbsp;)*Default: (.*)</td>`)

func TestGenMarkdownTreeCacheDirDefault(t *testing.T) {
	tests := map[string]struct {
		home         string
		kubeCacheDir string
		want         string
	}{
		"home without a passwd entry": {
			home: "/home-without-passwd-entry",
			want: "&#34;$HOME/.kube/cache&#34;",
		},
		"home with a trailing slash": {
			home: "/home-without-passwd-entry/",
			want: "&#34;$HOME/.kube/cache&#34;",
		},
		"home is the root directory": {
			home: "/",
			want: "&#34;$HOME/.kube/cache&#34;",
		},
		"empty home": {
			want: "&#34;$HOME/.kube/cache&#34;",
		},
		"KUBECACHEDIR is absolute and home is empty": {
			kubeCacheDir: "/var/cache/kube",
			want:         "&#34;/var/cache/kube&#34;",
		},
		"KUBECACHEDIR is the home directory": {
			home:         "/home-without-passwd-entry",
			kubeCacheDir: "/home-without-passwd-entry",
			want:         "&#34;$HOME&#34;",
		},
		"KUBECACHEDIR outside the home directory": {
			home:         "/home-without-passwd-entry",
			kubeCacheDir: "/var/cache/kube",
			want:         "&#34;/var/cache/kube&#34;",
		},
		"KUBECACHEDIR in a sibling that shares the home prefix": {
			home:         "/home-without-passwd-entry",
			kubeCacheDir: "/home-without-passwd-entry2/.kube/cache",
			want:         "&#34;/home-without-passwd-entry2/.kube/cache&#34;",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			t.Setenv("KUBECACHEDIR", tt.kubeCacheDir)
			cmd := &cobra.Command{Use: "kueuectl"}
			genericclioptions.NewConfigFlags(true).AddFlags(cmd.PersistentFlags())
			outputDir := t.TempDir()

			if err := GenMarkdownTree(cmd, "../templates", outputDir); err != nil {
				t.Fatalf("GenMarkdownTree() error: %v", err)
			}
			page, err := os.ReadFile(filepath.Join(outputDir, "kueuectl.md"))
			if err != nil {
				t.Fatalf("failed to read the generated page: %v", err)
			}
			match := cacheDirDefault.FindSubmatch(page)
			if match == nil {
				t.Fatalf("the generated page has no --cache-dir default:\n%s", page)
			}
			if diff := cmp.Diff(tt.want, string(match[1])); diff != "" {
				t.Errorf("--cache-dir default mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
