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

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type remoteController struct {
	localClient   client.Client
	watchCtx      context.Context
	watchCancel   context.CancelFunc
	remoteClients map[string]*remoteClient
	wlUpdateCh    chan<- event.GenericEvent

	// For unit testing only. There is now need of creating fully functional remote clients in the unit tests
	// and creating valid kubeconfig content is not trivial.
	// The full client creation and usage is validated in the integration and e2e tests.
	restConfigFromKubeConfigOverride func([]byte) (*rest.Config, error)
	newWithWatchOverride             func(config *rest.Config, options client.Options) (client.WithWatch, error)
}

func newRemoteController(watchCtx context.Context, localClient client.Client, wlUpdateCh chan<- event.GenericEvent) *remoteController {
	watchCtx, watchCancel := context.WithCancel(watchCtx)
	ret := &remoteController{
		localClient: localClient,
		watchCtx:    watchCtx,
		watchCancel: watchCancel,
		wlUpdateCh:  wlUpdateCh,
	}
	return ret
}

func (rc *remoteController) UpdateConfig(kubeConfigs map[string][]byte) error {
	if rc.remoteClients == nil {
		rc.remoteClients = make(map[string]*remoteClient, len(kubeConfigs))
	}

	for clusterName, c := range rc.remoteClients {
		if kubeconfig, found := kubeConfigs[clusterName]; found {
			if err := c.setConfig(kubeconfig); err != nil {
				delete(rc.remoteClients, clusterName)
				return err
			}
		} else {
			c.watchCancel()
			delete(rc.remoteClients, clusterName)
		}
	}
	// create the missing ones
	for clusterName, kubeconfig := range kubeConfigs {
		if _, found := rc.remoteClients[clusterName]; !found {
			c := newRemoteClient(rc.watchCtx, rc.localClient, rc.wlUpdateCh)

			c.restConfigFromKubeConfigOverride = rc.restConfigFromKubeConfigOverride
			c.newWithWatchOverride = rc.newWithWatchOverride

			if err := c.setConfig(kubeconfig); err != nil {
				return err
			}
			rc.remoteClients[clusterName] = c
		}
	}

	return nil
}

func (cc *remoteController) IsActive() bool {
	return cc != nil && len(cc.remoteClients) > 0
}

type remoteClient struct {
	localClient  client.Client
	client       client.WithWatch
	wlUpdateCh   chan<- event.GenericEvent
	rootWatchCtx context.Context
	watchCancel  context.CancelFunc
	watchItf     watch.Interface
	kubeconfig   []byte

	// For testing only.
	restConfigFromKubeConfigOverride func([]byte) (*rest.Config, error)
	newWithWatchOverride             func(config *rest.Config, options client.Options) (client.WithWatch, error)
}

func newRemoteClient(watchCtx context.Context, localClient client.Client, wlUpdateCh chan<- event.GenericEvent) *remoteClient {
	rc := &remoteClient{
		wlUpdateCh:   wlUpdateCh,
		localClient:  localClient,
		rootWatchCtx: watchCtx,
	}

	return rc
}

// setConfig - will try to recreate the k8s client and restart watching if the new config is different than
// the one currently used.
func (rc *remoteClient) setConfig(kubeconfig []byte) error {
	if equality.Semantic.DeepEqual(kubeconfig, rc.kubeconfig) {
		return nil
	}

	if rc.watchCancel != nil {
		rc.watchCancel()
		rc.watchCancel = nil
	}

	var err error
	var cfg *rest.Config

	if rc.restConfigFromKubeConfigOverride != nil {
		cfg, err = rc.restConfigFromKubeConfigOverride(kubeconfig)
	} else {
		cfg, err = clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	}
	if err != nil {
		return err
	}

	var remoteClient client.WithWatch
	if rc.newWithWatchOverride != nil {
		remoteClient, err = rc.newWithWatchOverride(cfg, client.Options{Scheme: rc.localClient.Scheme()})
	} else {
		remoteClient, err = client.NewWithWatch(cfg, client.Options{Scheme: rc.localClient.Scheme()})
	}
	if err != nil {
		return err
	}
	rc.client = remoteClient

	watchCtx, watchCancel := context.WithCancel(rc.rootWatchCtx)
	witf, err := rc.client.Watch(watchCtx, &kueue.WorkloadList{})
	if err != nil {
		watchCancel()
		return nil
	}
	rc.watchItf = witf
	rc.watchCancel = watchCancel

	go func() {
		for r := range witf.ResultChan() {
			rc.queueWorkloadEvent(watchCtx, r)
		}
	}()

	rc.kubeconfig = kubeconfig
	return nil
}

func (rc *remoteClient) queueWorkloadEvent(ctx context.Context, ev watch.Event) {
	wl, isWl := ev.Object.(*kueue.Workload)
	if !isWl {
		return
	}

	localWl := &kueue.Workload{}
	if err := rc.localClient.Get(ctx, client.ObjectKeyFromObject(wl), localWl); err == nil {
		rc.wlUpdateCh <- event.GenericEvent{Object: localWl}
	}
}
