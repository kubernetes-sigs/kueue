//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || ios || linux || netbsd || openbsd || solaris || zos

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

package multikueue

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadKubeConfigFile(t *testing.T) {
	tests := map[string]struct {
		setup       func(*testing.T) string
		wantSize    int
		wantContent []byte
		wantErr     string
	}{
		"regular file": {
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "worker.kubeconfig")
				contents := []byte("apiVersion: v1")
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				return path
			},
			wantSize:    len("apiVersion: v1"),
			wantContent: []byte("apiVersion: v1"),
		},
		"file at size limit": {
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "worker.kubeconfig")
				if err := os.WriteFile(path, make([]byte, maxKubeConfigSize), 0600); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				return path
			},
			wantSize: maxKubeConfigSize,
		},
		"file over size limit": {
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "worker.kubeconfig")
				if err := os.WriteFile(path, make([]byte, maxKubeConfigSize+1), 0600); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				return path
			},
			wantErr: "size limit",
		},
		"directory": {
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: "regular file",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			contents, err := readKubeConfigFile(tc.setup(t))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("readKubeConfigFile() error = %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readKubeConfigFile() error = %v", err)
			}
			if len(contents) != tc.wantSize {
				t.Fatalf("readKubeConfigFile() size = %d, want %d", len(contents), tc.wantSize)
			}
			if tc.wantContent != nil && !bytes.Equal(contents, tc.wantContent) {
				t.Fatalf("readKubeConfigFile() contents = %q, want %q", contents, tc.wantContent)
			}
		})
	}
}

func TestReadKubeConfigFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("os.Symlink() error = %v", err)
	}

	if _, err := readKubeConfigFile(link); err == nil {
		t.Fatal("readKubeConfigFile() error = nil, want symlink rejection")
	}
}

func TestReadKubeConfigFileRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "kubeconfig.fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatalf("unix.Mkfifo() error = %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := readKubeConfigFile(fifo)
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("readKubeConfigFile() error = %v, want regular-file rejection", err)
		}
	case <-time.After(2 * time.Second):
		// Unblock implementations that open FIFOs in blocking mode before
		// failing the test, so the regression does not leak a goroutine.
		writerDone := make(chan error, 1)
		go func() {
			writerDone <- os.WriteFile(fifo, []byte("not a kubeconfig"), 0600)
		}()
		select {
		case <-result:
		case <-time.After(2 * time.Second):
		}
		select {
		case <-writerDone:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("readKubeConfigFile() blocked while opening a FIFO")
	}
}
