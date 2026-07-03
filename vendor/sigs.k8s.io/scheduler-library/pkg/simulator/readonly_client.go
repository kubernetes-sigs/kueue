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

type ReadonlyClient struct {
	client kubernetes.Interface
}

func NewReadonlyClient(config *rest.Config) (ReadonlyClient, error) {
	if config == nil {
		return ReadonlyClient{}, fmt.Errorf("got nil config")
	}
	readonlyConfig := rest.CopyConfig(config)
	readonlyConfig.Wrap(readonlyRoundTripperFactory)
	client, err := kubernetes.NewForConfig(readonlyConfig)
	return ReadonlyClient{client: client}, err
}

type readonlyRoundTripper struct {
	rt http.RoundTripper
}

func (c *readonlyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return nil, fmt.Errorf("mutations are not supported in scheduler library")
	}
	return c.rt.RoundTrip(req)
}

func readonlyRoundTripperFactory(rt http.RoundTripper) http.RoundTripper {
	return &readonlyRoundTripper{rt: rt}
}
