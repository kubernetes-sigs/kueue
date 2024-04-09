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
	"path"
	"sync"

	"github.com/fsnotify/fsnotify"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

var (
	errNotStarted = errors.New("not started")
)

type KubeConfigFSWatcher struct {
	watcher       *fsnotify.Watcher
	lock          sync.RWMutex
	clusterToFile map[string]string
	// a single file can be potentially used by multiple clusters
	fileToClusters map[string]set.Set[string]
	parentDirs     map[string]set.Set[string]
	reconcile      chan event.GenericEvent
}

var _ manager.Runnable = (*KubeConfigFSWatcher)(nil)

func newKubeConfigFSWatcher() *KubeConfigFSWatcher {
	return &KubeConfigFSWatcher{
		clusterToFile:  map[string]string{},
		fileToClusters: map[string]set.Set[string]{},
		parentDirs:     map[string]set.Set[string]{},
		reconcile:      make(chan event.GenericEvent),
	}
}

func (w *KubeConfigFSWatcher) Start(ctx context.Context) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	var err error
	w.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	go func() {
		log := ctrl.LoggerFrom(ctx).WithName("secret-fs-watcher")
		defer func() {
			err := w.watcher.Close()
			if err != nil {
				log.V(2).Error(err, "Closing kubeconfigs FS Watcher")
			}
		}()
		watchedEvents := fsnotify.Write | fsnotify.Create | fsnotify.Remove | fsnotify.Rename
		for {
			select {
			case ev := <-w.watcher.Events:
				log.V(5).Info("Got event", "name", ev.Name, "op", ev.Op)
				switch {
				case (ev.Op & watchedEvents) != 0:
					w.notifyPathWrite(ev.Name)
				default:
					log.V(2).Info("Ignore event", "name", ev.Name, "op", ev.Op)

				}
			case err := <-w.watcher.Errors:
				log.V(2).Error(err, "Kubeconfigs FS Watch")
			case <-ctx.Done():
				log.V(2).Info("End kubeconfigs FS Watch")
				return
			}
		}

	}()
	return nil
}

func (w *KubeConfigFSWatcher) notifyPathWrite(path string) {
	w.lock.RLock()
	clusters := w.fileToClusters[path].UnsortedList()
	w.lock.RUnlock()
	//TODO: maybe manage "..data" pattern
	for _, c := range clusters {
		w.reconcile <- event.GenericEvent{Object: &kueuealpha.MultiKueueCluster{ObjectMeta: metav1.ObjectMeta{Name: c}}}
	}
}

func (w *KubeConfigFSWatcher) getClusterPath(cluster string) (string, bool) {
	w.lock.RLock()
	defer w.lock.RUnlock()
	path, found := w.clusterToFile[cluster]
	return path, found
}

func (w *KubeConfigFSWatcher) set(cluster, kcPath string) error {
	w.lock.Lock()
	defer w.lock.Unlock()

	dir := path.Dir(kcPath)
	if _, found := w.parentDirs[dir]; !found {
		err := w.watcher.Add(dir)
		if err != nil {
			return err
		}
	}

	w.clusterToFile[cluster] = kcPath
	if _, found := w.fileToClusters[kcPath]; found {
		w.fileToClusters[kcPath].Insert(cluster)
	} else {
		w.fileToClusters[kcPath] = set.New(cluster)
	}
	if _, found := w.parentDirs[dir]; found {
		w.parentDirs[dir].Insert(kcPath)
	} else {
		w.parentDirs[dir] = set.New(kcPath)
		return w.watcher.Add(dir)
	}
	return nil
}

func (w *KubeConfigFSWatcher) remove(cluster string) {
	w.lock.Lock()
	defer w.lock.Unlock()

	kcPath := w.clusterToFile[cluster]
	dir := path.Dir(kcPath)

	file_removed := false
	if s, found := w.fileToClusters[kcPath]; found {
		s.Delete(cluster)
		if s.Len() == 0 {
			delete(w.fileToClusters, kcPath)
			file_removed = true
		}
	}

	if file_removed {
		if s, found := w.parentDirs[dir]; found {
			s.Delete(kcPath)
			if s.Len() == 0 {
				delete(w.parentDirs, dir)
			}
		}
	}
	delete(w.clusterToFile, cluster)
}

func (w *KubeConfigFSWatcher) cleanOldWatchDirs() error {
	w.lock.RLock()
	defer w.lock.RUnlock()

	var errs []error
	for _, dir := range w.watcher.WatchList() {
		if _, found := w.parentDirs[dir]; !found {
			err := w.watcher.Remove(dir)
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (w *KubeConfigFSWatcher) Started() bool {
	w.lock.RLock()
	defer w.lock.RUnlock()
	return w.watcher != nil
}

func (w *KubeConfigFSWatcher) AddOrUpdate(cluster, path string) error {
	if !w.Started() {
		return errNotStarted
	}
	cPath, found := w.getClusterPath(cluster)
	if found && path == cPath {
		return nil
	}

	if found {
		w.remove(cluster)
	}

	err := w.set(cluster, path)
	if err != nil {
		return err
	}

	return w.cleanOldWatchDirs()
}

func (w *KubeConfigFSWatcher) Remove(cluster string) error {
	if !w.Started() {
		return errNotStarted
	}
	_, found := w.getClusterPath(cluster)
	if !found {
		return nil
	}
	w.remove(cluster)
	return w.cleanOldWatchDirs()
}
