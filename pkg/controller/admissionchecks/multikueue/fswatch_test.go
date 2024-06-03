/*
Copyright 2024 The Kubernetes Authors.

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/set"
)

func TestFSWatch(t *testing.T) {
	cases := map[string]struct {
		skipOnDarwin bool
		prepareFnc   func(basePath string) error
		clusters     map[string]string
		opFnc        func(basePath string) error

		wantEventsForClusters set.Set[string]
	}{
		"empty": {},
		"single cluster existing file": {
			prepareFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig"), []byte("123"), 0666)
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig"), []byte("123456"), 0666)
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single cluster missing file": {
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig"), []byte("123456"), 0666)
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single existing file deleted": {
			prepareFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig"), []byte("123"), 0666)
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.Remove(filepath.Join(basePath, "c1.kubeconfig"))
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single cluster file rename to": {
			prepareFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig.old"), []byte("123"), 0666)
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.Rename(filepath.Join(basePath, "c1.kubeconfig.old"), filepath.Join(basePath, "c1.kubeconfig"))
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single cluster file rename from": {
			prepareFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig"), []byte("123"), 0666)
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.Rename(filepath.Join(basePath, "c1.kubeconfig"), filepath.Join(basePath, "c1.kubeconfig.new"))
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single create link": {
			prepareFnc: func(basePath string) error {
				return os.WriteFile(filepath.Join(basePath, "c1.kubeconfig.src"), []byte("123"), 0666)
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.Symlink(filepath.Join(basePath, "c1.kubeconfig.src"), filepath.Join(basePath, "c1.kubeconfig"))
			},
			wantEventsForClusters: set.New("c1"),
		},
		"single remove link": {
			// For some reason, on darwin platform, we don't take the symlink removal event.
			// It's not critical to test it on MacOS, so we can skip it for now until it's
			// fixed https://github.com/fsnotify/fsnotify/issues/636.
			skipOnDarwin: true,
			prepareFnc: func(basePath string) error {
				if err := os.WriteFile(filepath.Join(basePath, "c1.kubeconfig.src"), []byte("123"), 0666); err != nil {
					return err
				}
				return os.Symlink(filepath.Join(basePath, "c1.kubeconfig.src"), filepath.Join(basePath, "c1.kubeconfig"))
			},
			clusters: map[string]string{
				"c1": "c1.kubeconfig",
			},
			opFnc: func(basePath string) error {
				return os.Remove(filepath.Join(basePath, "c1.kubeconfig"))
			},
			wantEventsForClusters: set.New("c1"),
		},
	}

	for name, tc := range cases {
		if tc.skipOnDarwin && runtime.GOOS == "darwin" {
			continue
		}

		t.Run(name, func(t *testing.T) {
			basePath := t.TempDir()
			if tc.prepareFnc != nil {
				if err := tc.prepareFnc(basePath); err != nil {
					t.Fatalf("unexpected prepare error: %s", err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			watcher := newKubeConfigFSWatcher()

			// start the recorder
			gotEventsForClusters := set.New[string]()
			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case ev := <-watcher.reconcile:
						gotEventsForClusters.Insert(ev.Object.GetName())
						if len(gotEventsForClusters) == len(tc.wantEventsForClusters) {
							cancel()
						}
					case <-ctx.Done():
						return
					}
				}
			}()
			_ = watcher.Start(ctx)

			gotAddErrors := map[string]error{}
			for c, p := range tc.clusters {
				if err := watcher.AddOrUpdate(c, filepath.Join(basePath, p)); err != nil {
					gotAddErrors[c] = err
				}
			}

			if tc.opFnc != nil {
				if err := tc.opFnc(basePath); err != nil {
					t.Fatalf("unexpected op error: %s", err)
				}
			}
			// wait
			wg.Wait()

			if len(gotAddErrors) > 0 {
				t.Errorf("unexpected add errors:%s", gotAddErrors)
			}

			if diff := cmp.Diff(tc.wantEventsForClusters, gotEventsForClusters); diff != "" {
				t.Errorf("unexpected events for clusters(-want/+got):\n%s", diff)
			}
		})
	}
}

func TestFSWatchAddRm(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	basePath := t.TempDir()
	f1Path := filepath.Join(basePath, "file.one")
	f2Path := filepath.Join(basePath, "file.two")
	f3Dir := filepath.Join(basePath, "d1")
	f3Path := filepath.Join(f3Dir, "file.three")
	steps := []struct {
		name               string
		skipOnDarwin       bool
		opFnc              func(*KubeConfigFSWatcher) error
		wantOpErr          error
		wantClustersToFile map[string]string
		wantFileToClusters map[string]set.Set[string]
		wantParentDirs     map[string]set.Set[string]
		wantWatchList      []string
	}{
		{
			name: "add cluster before start",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c1", f1Path)
			},
			wantOpErr: errNotStarted,
		},
		{
			name: "start",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.Start(ctx)
			},
			wantClustersToFile: map[string]string{},
			wantFileToClusters: map[string]set.Set[string]{},
			wantParentDirs:     map[string]set.Set[string]{},
			wantWatchList:      []string{},
		},
		{
			name: "add first cluster",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c1", f1Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "add second cluster same path",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c2", f1Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "add first cluster again",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c1", f1Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "add third cluster, different file ",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c3", f2Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
				"c3": f2Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
				f2Path: set.New("c3"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path, f2Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "add fourth cluster, missing dir ",
			// For some reason on the Darwin platform, when we try to add an invalid directory,
			// an error is returned, but it is also added to the watchlist, so we don't have
			// the same result as we expected.
			// It's not critical to test it on MacOS, so we can skip it for now until it's
			// fixed https://github.com/fsnotify/fsnotify/issues/637.
			skipOnDarwin: true,
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c4", f3Path)
			},
			wantOpErr: os.ErrNotExist,
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
				"c3": f2Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
				f2Path: set.New("c3"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path, f2Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "create dir and add fourth cluster",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				err := os.Mkdir(f3Dir, os.ModePerm)
				if err != nil {
					return err
				}
				return kcf.AddOrUpdate("c4", f3Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
				"c3": f2Path,
				"c4": f3Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
				f2Path: set.New("c3"),
				f3Path: set.New("c4"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path, f2Path),
				f3Dir:    set.New(f3Path),
			},
			wantWatchList: []string{
				basePath,
				f3Dir,
			},
		},
		{
			name: "change the file for c3",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.AddOrUpdate("c3", f3Path)
			},
			wantClustersToFile: map[string]string{
				"c1": f1Path,
				"c2": f1Path,
				"c3": f3Path,
				"c4": f3Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c1", "c2"),
				f3Path: set.New("c4", "c3"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
				f3Dir:    set.New(f3Path),
			},
			wantWatchList: []string{
				basePath,
				f3Dir,
			},
		},
		{
			name: "remove three clusters",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return errors.Join(
					kcf.Remove("c1"),
					kcf.Remove("c3"),
					kcf.Remove("c4"),
				)
			},
			wantClustersToFile: map[string]string{
				"c2": f1Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c2"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "remove an unknown cluster",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.Remove("c1")
			},
			wantClustersToFile: map[string]string{
				"c2": f1Path,
			},
			wantFileToClusters: map[string]set.Set[string]{
				f1Path: set.New("c2"),
			},
			wantParentDirs: map[string]set.Set[string]{
				basePath: set.New(f1Path),
			},
			wantWatchList: []string{
				basePath,
			},
		},
		{
			name: "remove the last cluster",
			opFnc: func(kcf *KubeConfigFSWatcher) error {
				return kcf.Remove("c2")
			},
			wantClustersToFile: map[string]string{},
			wantFileToClusters: map[string]set.Set[string]{},
			wantParentDirs:     map[string]set.Set[string]{},
			wantWatchList:      []string{},
		},
	}

	w := newKubeConfigFSWatcher()

	for _, tc := range steps {
		if tc.skipOnDarwin && runtime.GOOS == "darwin" {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			gotErr := tc.opFnc(w)
			if diff := cmp.Diff(tc.wantOpErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error(-want/+got):\n%s", diff)
			}

			if tc.wantClustersToFile != nil {
				if diff := cmp.Diff(tc.wantClustersToFile, w.clusterToFile, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("unexpected clusterToFile(-want/+got):\n%s", diff)
				}
			}

			if tc.wantFileToClusters != nil {
				if diff := cmp.Diff(tc.wantFileToClusters, w.fileToClusters, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("unexpected fileToClusters(-want/+got):\n%s", diff)
				}
			}

			if tc.wantParentDirs != nil {
				if diff := cmp.Diff(tc.wantParentDirs, w.parentDirToFiles, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("unexpected parent dir(-want/+got):\n%s", diff)
				}
			}

			if tc.wantWatchList != nil {
				if diff := cmp.Diff(tc.wantWatchList, w.watcher.WatchList(), cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
					t.Errorf("unexpected watch list(-want/+got):\n%s", diff)
				}
			}
		})
	}
}
