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

package parallelize

import (
	"context"

	"k8s.io/client-go/util/workqueue"
)

const maxParallelism = 8

// ErrorChannel supports non-blocking send and receive operation to capture error.
// A maximum of one error is kept in the channel and the rest of the errors sent
// are ignored, unless the existing error is received and the channel becomes empty
// again.
type ErrorChannel struct {
	ch chan error
}

func NewErrorChannel() *ErrorChannel {
	return &ErrorChannel{
		ch: make(chan error, 1),
	}
}

func (e *ErrorChannel) SendError(err error) {
	if err == nil {
		return
	}
	select {
	case e.ch <- err:
	default:
	}
}

func (e *ErrorChannel) Receive() error {
	select {
	case err := <-e.ch:
		return err
	default:
		return nil
	}
}

func Until(ctx context.Context, pieces int, doWorkPiece func(i int) error) error {
	errCh := NewErrorChannel()
	workers := min(pieces, maxParallelism)
	workqueue.ParallelizeUntil(ctx, workers, pieces, func(i int) {
		errCh.SendError(doWorkPiece(i))
	})
	return errCh.Receive()
}
