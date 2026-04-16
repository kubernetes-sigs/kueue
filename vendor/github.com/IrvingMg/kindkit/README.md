# kindkit

[![Go Reference](https://pkg.go.dev/badge/github.com/IrvingMg/kindkit.svg)](https://pkg.go.dev/github.com/IrvingMg/kindkit)
[![CI](https://github.com/IrvingMg/kindkit/actions/workflows/ci.yaml/badge.svg)](https://github.com/IrvingMg/kindkit/actions/workflows/ci.yaml)

Go library for managing [Kind](https://kind.sigs.k8s.io/) clusters in test workflows.

## Why kindkit?

Go test suites that run against Kind clusters often shell out to the `kind` CLI for cluster lifecycle, making failure handling, cleanup, and debugging harder to keep consistent.

Even when using Kind's Go packages directly, projects tend to reimplement the same helpers: readiness checks, kubeconfig handling, reuse logic, and teardown.

kindkit provides a small, focused API for these lifecycle operations, so you can manage clusters directly from Go test code without rebuilding the same plumbing each time.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)

The `kind` CLI is not required. kindkit manages clusters through Kind's Go packages.

## Install

```bash
go get github.com/IrvingMg/kindkit
```

## Quick example

Create a Kind cluster, get a REST config, and load images — all from Go:

```go
ctx := context.Background()

cluster, err := kindkit.Create(ctx, "my-test-cluster",
    kindkit.WithWaitForReady(3*time.Minute),
)
if err != nil {
    // Partial failure: creation failed but a cluster was returned.
    // Export logs for debugging, then clean up.
    if cluster != nil {
        _ = cluster.ExportLogs(ctx, "./test-logs")
        _ = cluster.Delete(ctx)
    }
    log.Fatal(err)
}
defer cluster.Delete(ctx)

restCfg, err := cluster.RESTConfig()
if err != nil {
    log.Fatal(err)
}

if err := cluster.LoadImages(ctx, "my-app:latest"); err != nil {
    log.Fatal(err)
}
```

## API

The public API covers cluster creation, configuration access, and common operations:

| Function | Description |
|---|---|
| `Create(ctx, name, opts...)` | Create a cluster. Returns both `*Cluster` and error on partial failure. |
| `CreateOrReuse(ctx, name, opts...)` | Reuse an existing cluster if reachable, otherwise create a new one. |
| `Cluster.Delete(ctx)` | Delete the cluster. Idempotent. |
| `Cluster.RESTConfig()` | Get a `*rest.Config` for the cluster. |
| `Cluster.KubeconfigPath()` | Write kubeconfig to a temp file and return its path. |
| `Cluster.LoadImages(ctx, images...)` | Load local Docker images into all cluster nodes. |
| `Cluster.ExportLogs(ctx, dir)` | Export cluster logs to a directory for debugging. |
| `Cluster.ApplyManifests(ctx, yaml)` | Apply multi-document Kubernetes YAML via server-side apply. |
| `WithNodeImage(image)` | Option: set the Kind node image. |
| `WithWaitForReady(d)` | Option: set the readiness timeout. |
| `WithRawConfig(raw)` | Option: pass raw Kind cluster config YAML for full control over cluster topology. Mutually exclusive with `WithConfigFile`. |
| `WithConfigFile(path)` | Option: load Kind cluster config from a file path. Mutually exclusive with `WithRawConfig`. |

## Examples

Complete runnable examples are available in the `examples/` directory:

- [`examples/basic/`](examples/basic/) — cluster lifecycle: create, use, load images, tear down.
- [`examples/operator-testing/`](examples/operator-testing/) — deploy cert-manager and verify it issues a certificate.

Run all examples with:

```bash
make test-examples # Requires Docker
```

## Compatibility

kindkit is tested against the Kind versions that target [currently-supported Kubernetes releases](https://kubernetes.io/releases/). Each Kind release supports multiple Kubernetes versions — see the [Kind releases page](https://github.com/kubernetes-sigs/kind/releases) for details. 

The exact Kind versions tested are defined in the [CI workflow](.github/workflows/ci.yaml).

## Running tests

The project includes unit and end-to-end tests. E2e tests create real Kind clusters and require Docker.

```bash
make test          # unit + e2e tests
make test-unit     # unit tests only
make test-e2e      # e2e tests only (requires Docker)
```

## Contributing

Feedback and contributions are welcome — open an issue, start a discussion, or submit a pull request.

## License

This project is licensed under the [Apache License 2.0](LICENSE).
