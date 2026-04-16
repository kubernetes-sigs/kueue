package kindkit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/client"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
)

// LoadImages loads images from the local Docker daemon into all
// cluster nodes. The images must already exist locally. On a
// multi-node cluster, per-node failures are collected and returned
// together via errors.Join.
func (c *Cluster) LoadImages(ctx context.Context, images ...string) error {
	if len(images) == 0 {
		return nil
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer dockerClient.Close() //nolint:errcheck // best-effort cleanup; no data to flush

	clusterNodes, err := c.provider.ListInternalNodes(c.name)
	if err != nil {
		return fmt.Errorf("list nodes for cluster %q: %w", c.name, err)
	}

	var errs []error
	for _, node := range clusterNodes {
		rc, err := dockerClient.ImageSave(ctx, images)
		if err != nil {
			// Images not available locally; no point trying remaining nodes.
			return fmt.Errorf("save images: %w", err)
		}
		err = loadImageArchive(node, rc)
		rc.Close() //nolint:errcheck // best-effort cleanup; data already consumed
		if err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", node.String(), err))
		}
	}
	return errors.Join(errs...)
}

func loadImageArchive(n nodes.Node, archive io.Reader) error {
	return n.Command(
		"ctr", "--namespace=k8s.io",
		"images", "import",
		"--digests",
		"-",
	).SetStdin(archive).Run()
}
