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

package kubeversion

import (
	"context"
	"sync"
	"time"

	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
)

const fetchServerVersionInterval = time.Minute * 10

type ServerVersionFetcher struct {
	dc            discovery.DiscoveryInterface
	ticker        *time.Ticker
	serverVersion *versionutil.Version
	rwm           sync.RWMutex
}

func NewServerVersionFetcher(dc discovery.DiscoveryInterface) *ServerVersionFetcher {
	return &ServerVersionFetcher{
		dc:            dc,
		ticker:        time.NewTicker(fetchServerVersionInterval),
		serverVersion: &versionutil.Version{},
	}
}

// Start implements the Runnable interface to run ServerVersionFetcher as a controller.
func (s *ServerVersionFetcher) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("serverVersionFetcher")
	ctx = ctrl.LoggerInto(ctx, log)

	if err := s.FetchServerVersion(); err != nil {
		log.Error(err, "Unable to fetch server version")
	}

	defer s.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.V(5).Info("Context cancelled; stop fetching server version")
			return nil
		case <-s.ticker.C:
			if err := s.FetchServerVersion(); err != nil {
				log.Error(err, "Unable to fetch server version")
			} else {
				log.V(5).Info("Fetch server version", "serverVersion", s.serverVersion)
			}
		}
	}
}

// FetchServerVersion gets API server version
func (s *ServerVersionFetcher) FetchServerVersion() error {
	v, err := FetchServerVersion(s.dc)
	if err != nil {
		return err
	}
	s.rwm.Lock()
	defer s.rwm.Unlock()
	s.serverVersion = v
	return nil
}

func (s *ServerVersionFetcher) GetServerVersion() versionutil.Version {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return *s.serverVersion
}

func FetchServerVersion(d discovery.DiscoveryInterface) (*versionutil.Version, error) {
	clusterVersionInfo, err := d.ServerVersion()
	if err != nil {
		return nil, err
	}
	return versionutil.ParseSemantic(clusterVersionInfo.String())
}
