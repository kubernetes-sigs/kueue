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

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type fakeSyncingSource struct {
	syncErr error
}

func (s *fakeSyncingSource) Start(context.Context, workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	return nil
}

func (s *fakeSyncingSource) WaitForSync(context.Context) error {
	return s.syncErr
}

func waitReturned(ctx context.Context, cache *Cache) <-chan error {
	done := make(chan error, 1)
	go func() { done <- cache.WaitForInitialSync(ctx) }()
	return done
}

func expectBlocked(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("WaitForInitialSync() returned %v while a tracked source had not synced", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func expectReturned(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("WaitForInitialSync() = %v, want %v", err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForInitialSync() did not return")
	}
}

func TestWaitForInitialSync(t *testing.T) {
	t.Run("no tracked sources", func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		expectReturned(t, waitReturned(ctx, New(utiltesting.NewFakeClient())), nil)
	})

	t.Run("waits for every tracked source", func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		cache := New(utiltesting.NewFakeClient())
		first := cache.TrackInitialSync(&fakeSyncingSource{})
		second := cache.TrackInitialSync(&fakeSyncingSource{})
		done := waitReturned(ctx, cache)

		if err := first.WaitForSync(ctx); err != nil {
			t.Fatalf("first.WaitForSync() = %v", err)
		}
		// A second call on a synced source must not count for another source.
		if err := first.WaitForSync(ctx); err != nil {
			t.Fatalf("first.WaitForSync() again = %v", err)
		}
		expectBlocked(t, done)

		if err := second.WaitForSync(ctx); err != nil {
			t.Fatalf("second.WaitForSync() = %v", err)
		}
		expectReturned(t, done, nil)
		// Later waits return at once.
		expectReturned(t, waitReturned(ctx, cache), nil)
	})

	t.Run("a source that fails to sync stays pending until the context ends", func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		ctx, cancel := context.WithCancel(ctx)
		cache := New(utiltesting.NewFakeClient())
		syncErr := errors.New("cache did not sync")
		failing := cache.TrackInitialSync(&fakeSyncingSource{syncErr: syncErr})
		done := waitReturned(ctx, cache)

		if err := failing.WaitForSync(ctx); !errors.Is(err, syncErr) {
			t.Fatalf("WaitForSync() = %v, want %v", err, syncErr)
		}
		expectBlocked(t, done)
		cancel()
		expectReturned(t, done, context.Canceled)
	})
}
