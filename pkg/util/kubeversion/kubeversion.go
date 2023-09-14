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

var (
	KubeVersion1_27 = versionutil.MustParseSemantic("1.27.0")
)

type ServerVersionFetcher struct {
	dc            discovery.DiscoveryInterface
	ticker        *time.Ticker
	serverVersion *versionutil.Version
	rwm           sync.RWMutex
}

type Options struct {
	Interval time.Duration
}

type Option func(*Options)

var defaultOptions = Options{
	Interval: fetchServerVersionInterval,
}

func WithInterval(interval time.Duration) Option {
	return func(o *Options) {
		o.Interval = interval
	}
}

func NewServerVersionFetcher(dc discovery.DiscoveryInterface, opts ...Option) *ServerVersionFetcher {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &ServerVersionFetcher{
		dc:            dc,
		ticker:        time.NewTicker(options.Interval),
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
	clusterVersionInfo, err := s.dc.ServerVersion()
	if err != nil {
		return err
	}
	s.rwm.Lock()
	defer s.rwm.Unlock()
	serverVersion, err := versionutil.ParseSemantic(clusterVersionInfo.String())
	if err == nil {
		s.serverVersion = serverVersion
	}
	return err
}

func (s *ServerVersionFetcher) GetServerVersion() versionutil.Version {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	return *s.serverVersion
}
