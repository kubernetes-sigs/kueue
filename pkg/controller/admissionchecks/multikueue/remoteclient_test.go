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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errInvalidConfig = errors.New("invalid kubeconfig")
)

func fakeClientBuilder(kubeconfig []byte, options client.Options) (client.WithWatch, error) {
	if string(kubeconfig) == "invalid" {
		return nil, errInvalidConfig
	}
	b, _ := getClientBuilder()
	return b.Build(), nil
}

func newTestClient(config string) *remoteClient {
	b, _ := getClientBuilder()
	localClient := b.Build()
	ret := &remoteClient{
		kubeconfig:  []byte(config),
		localClient: localClient,

		builderOverride: fakeClientBuilder,
	}
	ret.watchCancel = func() {
		ret.kubeconfig = []byte(string(ret.kubeconfig) + " canceled")
	}
	return ret
}

func TestUpdateConfig(t *testing.T) {
	cases := map[string]struct {
		kubeconfigs   map[string][]byte
		remoteClients map[string]*remoteClient

		wantRemoteClients map[string]*remoteClient
		wantError         error
	}{
		"new valid client is added": {
			kubeconfigs: map[string][]byte{
				"worker1": []byte("worker1 kubeconfig"),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with valid config": {
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			kubeconfigs: map[string][]byte{
				"worker1": []byte("worker1 kubeconfig"),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
		"update client with invalid config": {
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 old kubeconfig"),
			},
			kubeconfigs: map[string][]byte{
				"worker1": []byte("invalid"),
			},
			wantError: errInvalidConfig,
		},
		"missing cluster is removed": {
			remoteClients: map[string]*remoteClient{
				"worker1": newTestClient("worker1 kubeconfig"),
				"worker2": newTestClient("worker2 kubeconfig"),
			},
			kubeconfigs: map[string][]byte{
				"worker1": []byte("worker1 kubeconfig"),
			},
			wantRemoteClients: map[string]*remoteClient{
				"worker1": {
					kubeconfig: []byte("worker1 kubeconfig"),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()
			c := builder.Build()

			remCtrl := newRemoteController(ctx, c, nil)
			if len(tc.remoteClients) > 0 {
				remCtrl.remoteClients = tc.remoteClients
			}

			remCtrl.builderOverride = fakeClientBuilder

			gotErr := remCtrl.UpdateConfig(tc.kubeconfigs)
			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantRemoteClients, remCtrl.remoteClients, cmpopts.EquateEmpty(),
				cmpopts.IgnoreFields(remoteController{}, "localClient", "watchCancel", "watchCtx", "wlUpdateCh"),
				cmp.Comparer(func(a, b remoteClient) bool { return string(a.kubeconfig) == string(b.kubeconfig) })); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}
		})
	}
}
