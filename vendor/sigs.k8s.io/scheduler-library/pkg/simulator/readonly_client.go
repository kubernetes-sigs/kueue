// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package simulator

import (
	"fmt"
	"net/http"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ReadonlyClient is the only way for a library consumer to hand a Kubernetes client to
// NewSchedulingSimulator. It wraps a kubernetes.Interface whose transport rejects every
// mutating request, which guarantees that a simulation can never modify the real cluster.
//
// The library needs a client because the scheduler framework and several plugins take one
// (directly or through the informers) to read the cluster state. Since the wrapped client is
// unexported and can only be produced by NewReadonlyClient from a *rest.Config, consumers
// cannot substitute a client that writes.
//
// The transport-level rejection is a safety net rather than a functional requirement: with a
// correct implementation no mutating request is ever issued (see the no-op event recorder and
// APICacher in pkg/framework). Tests, which live inside the library, construct ReadonlyClient
// directly with a fake client instead of going through NewReadonlyClient.
type ReadonlyClient struct {
	client kubernetes.Interface
}

// NewReadonlyClient builds a ReadonlyClient from the given rest config. The config is copied,
// so the caller's config is left untouched.
func NewReadonlyClient(config *rest.Config) (ReadonlyClient, error) {
	if config == nil {
		return ReadonlyClient{}, fmt.Errorf("got nil config")
	}
	readonlyConfig := rest.CopyConfig(config)
	readonlyConfig.Wrap(readonlyRoundTripperFactory)
	client, err := kubernetes.NewForConfig(readonlyConfig)
	return ReadonlyClient{client: client}, err
}

// readonlyRoundTripper fails any request that could mutate the cluster before it leaves the process.
type readonlyRoundTripper struct {
	rt http.RoundTripper
}

// RoundTrip rejects the HTTP methods used by the create, update, patch and delete verbs,
// and passes everything else (get, list, watch) to the wrapped transport.
func (c *readonlyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return nil, fmt.Errorf("mutations are not supported in scheduler library")
	}
	return c.rt.RoundTrip(req)
}

// readonlyRoundTripperFactory adapts readonlyRoundTripper to rest.Config.Wrap.
func readonlyRoundTripperFactory(rt http.RoundTripper) http.RoundTripper {
	return &readonlyRoundTripper{rt: rt}
}
