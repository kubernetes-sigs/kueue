package kindkit

import (
	"fmt"
	"time"

	"sigs.k8s.io/kind/pkg/cluster"
)

// Option configures cluster creation. Options are passed to [Create]
// and [CreateOrReuse].
type Option func(*options)

type options struct {
	nodeImage    string
	waitForReady time.Duration
	rawConfig    []byte
	configFile   string
}

// WithNodeImage sets the node Docker image (e.g. "kindest/node:v1.31.0").
// When unset, Kind uses the default node image built into its library
// (the specific image varies by Kind version).
func WithNodeImage(image string) Option {
	return func(o *options) {
		o.nodeImage = image
	}
}

// WithWaitForReady sets the timeout for waiting for the cluster to be
// ready. When unset, Create returns as soon as Kind finishes
// provisioning the node containers, without waiting for the control
// plane to accept requests; callers that use the cluster immediately
// should supply a non-zero duration.
func WithWaitForReady(d time.Duration) Option {
	return func(o *options) {
		o.waitForReady = d
	}
}

// WithRawConfig passes a raw Kind cluster configuration YAML
// (kind: Cluster, apiVersion: kind.x-k8s.io/v1alpha4) to Kind.
// Mutually exclusive with WithConfigFile.
func WithRawConfig(raw []byte) Option {
	return func(o *options) {
		o.rawConfig = raw
	}
}

// WithConfigFile loads a Kind cluster configuration from a file path.
// Mutually exclusive with WithRawConfig.
func WithConfigFile(path string) Option {
	return func(o *options) {
		o.configFile = path
	}
}

func applyOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func buildCreateOptions(o options) ([]cluster.CreateOption, error) {
	if len(o.rawConfig) > 0 && o.configFile != "" {
		return nil, fmt.Errorf("WithRawConfig and WithConfigFile are mutually exclusive")
	}

	var copts []cluster.CreateOption

	copts = append(copts,
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	)

	if len(o.rawConfig) > 0 {
		copts = append(copts, cluster.CreateWithRawConfig(o.rawConfig))
	}

	if o.configFile != "" {
		copts = append(copts, cluster.CreateWithConfigFile(o.configFile))
	}

	if o.nodeImage != "" {
		copts = append(copts, cluster.CreateWithNodeImage(o.nodeImage))
	}

	if o.waitForReady > 0 {
		copts = append(copts, cluster.CreateWithWaitForReady(o.waitForReady))
	}

	return copts, nil
}
