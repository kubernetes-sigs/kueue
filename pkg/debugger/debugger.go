/*
Copyright 2023 The Kubernetes Authors.

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

package debugger

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

type Dumper struct {
	cache  *cache.Cache
	queues *queue.Manager
}

func NewDumper(c *cache.Cache, q *queue.Manager) *Dumper {
	return &Dumper{cache: c, queues: q}
}

// ListenForSignal starts a goroutine that will trigger the Dumper's
// behavior when the process receives SIGUSR2.
func (d *Dumper) ListenForSignal(ctx context.Context) {
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGUSR2)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-signalCh:
				d.DumpAll(ctx)
			}
		}
	}()
}

func (d *Dumper) DumpAll(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithName("dumper")
	log.Info("Started dump")
	defer func() {
		log.Info("Ended dump")
	}()
	if snap, err := d.cache.Snapshot(ctx); err != nil {
		log.Error(err, "unexpected error while building snapshot")
	} else {
		snap.Log(log)
	}
	d.queues.LogDump(log)
}
