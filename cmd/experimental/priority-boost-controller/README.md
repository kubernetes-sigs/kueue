# Priority Boost Controller

The `priority-boost-controller` is an experimental controller that computes
and updates `kueue.x-k8s.io/priority-boost` on Kueue Workload objects.

## Purpose

This component demonstrates an external policy controller for KEP-7990
priority boost signal. It is not part of the main Kueue binary and is
intended to be built and deployed independently.

The controller watches Workload objects and sets the `priority-boost`
annotation based on the number of prior preemptions recorded in
`schedulingStats`. To limit preemption flapping, the boost is incremented
only on every 2nd preemption (threshold-based approach).

## Build

To build the `priority-boost-controller` binary:

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

- `maxBoost`: upper bound for the computed priority boost value.

Default config is defined in `config/manager/controller_config_map.yaml`.

## Helm

A Helm chart is available at:

`charts/priority-boost-controller`
