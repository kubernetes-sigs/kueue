# Preemption Cost Controller

The `preemption-cost-controller` is an experimental controller that computes
and updates `kueue.x-k8s.io/preemption-cost` on Kueue-managed Jobs.

## Purpose

This component demonstrates an external policy controller for KEP-7990
preemption cost signal. It is not part of the main Kueue binary and is
intended to be built and deployed independently.

## Build

To build the `preemption-cost-controller` binary:

```bash
make build
```

To run controller tests:

```bash
make test
```

To build the container image:

```bash
make image-build
```

## Deploy

Apply manifests from `config/`:

```bash
kubectl apply -k config
```

## Configuration

The controller reads configuration from `--config` YAML file:

- `enableRunningPhaseIncrement`: add phase increment when Job has active pods.
- `runningPhaseIncrement`: increment value added for active Jobs.
- `maxCost`: upper bound for the computed preemption cost.

Default config is defined in `config/manager/controller_config_map.yaml`.

## Helm

A Helm chart is available at:

`charts/preemption-cost-controller`
