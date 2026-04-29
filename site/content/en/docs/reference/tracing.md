---
title: "OpenTelemetry tracing"
linkTitle: "Tracing"
date: 2026-04-29
description: >
  Optional distributed tracing for Workload reconciliation using OpenTelemetry OTLP.
---

Kueue can export **OpenTelemetry** traces for the [Workload](/docs/concepts/workload) controller when tracing is enabled in the Kueue configuration.

Tracing complements [Prometheus metrics](/docs/reference/metrics); it does not replace them.

## Enable tracing

In the Kueue `Configuration` object (same file as other controller settings), set:

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
tracing:
  enable: true
  # Optional: fraction of new trace roots to sample when there is no remote parent (0.0–1.0). Default: 0.01 (1%).
  samplingRatio: 0.01
```

When `tracing.enable` is `false` or unset, the controller uses a no-op tracer (no overhead).

## Exporter and environment variables

Kueue uses the OTLP **gRPC** trace exporter. Configure the backend with the standard OpenTelemetry environment variables, for example:

| Variable | Purpose |
| -------- | ------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector address (host:port or URI). |
| `OTEL_EXPORTER_OTLP_INSECURE` | Set to `true` when the endpoint has no TLS (typical in-cluster HTTP). |
| `OTEL_EXPORTER_OTLP_HEADERS` | Optional `key=value` headers (comma-separated) for authenticated collectors. |

See the [OpenTelemetry Go SDK documentation](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/) for the full set of OTLP options.

## Trace context on Workloads

When a trace is **recording**, Kueue may persist W3C Trace Context on the Workload as the annotation `kueue.x-k8s.io/trace-context` (JSON map of `traceparent` / `tracestate`). The next reconcile continues the same trace. This is independent of Kubernetes **Events** (`kubectl get events`), which remain human-oriented signals and are not a trace transport.

## Kubernetes Events and apiserver tracing

- **`core/v1` Events** emitted by Kueue (for example on admission check failure) are not OpenTelemetry spans. You can still correlate manually by matching timestamps or by including a trace id in an event message in future tooling; that is not done by default.
- **kube-apiserver tracing** (cluster admin feature) traces API requests. Kueue workload spans are separate unless you explicitly propagate trace context into API calls (advanced).

## Example: sidecar collector

A common pattern is to run an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) as a sidecar in the Kueue controller pod and set `OTEL_EXPORTER_OTLP_ENDPOINT` to `localhost:4317`, then forward to Jaeger, Grafana Tempo, or a vendor backend.

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "127.0.0.1:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
```

Tune `samplingRatio` (or rely on your collector’s tail sampling) to control volume in large clusters.

## Local Jaeger (development)

For a quick backend on your workstation, use the Compose file in the repository:

```bash
docker compose -f hack/tracing/docker-compose.yaml up -d
```

- **Jaeger UI:** `http://localhost:16686`
- **OTLP gRPC:** port `4317` (matches Kueue’s OTLP gRPC exporter)

When running the **Kueue binary on the same host** (not inside Kubernetes), you can point at the mapped port:

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "127.0.0.1:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
```

See [`hack/tracing/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/hack/tracing/README.md) for details. The Jaeger all-in-one image receives OTLP directly; add an OpenTelemetry Collector only if you need the same processors as production.

## End-to-end tests and cluster networking

End-to-end tests run against a **real cluster** where Kueue is already installed; tracing is configured on the **controller Deployment** (same `Configuration` CRD and `OTEL_*` env vars as production). The test process does not start the OTLP exporter itself.

**Important:** Inside a pod, `127.0.0.1` is the pod’s loopback, not the host. Pick one way to make OTLP reachable:

1. **Run Jaeger (or a Collector) inside the cluster** and set `OTEL_EXPORTER_OTLP_ENDPOINT` to that **Service** DNS name and port (for example `otel-collector.observability.svc.cluster.local:4317`), with `OTEL_EXPORTER_OTLP_INSECURE=true` when using plaintext.

2. **Run Jaeger on the host** (for example via `hack/tracing/docker-compose.yaml`) and reach it from cluster nodes:
   - **kind:** add `extraPortMappings` on the node so host port `4317` forwards into the node, then use the host’s address from the pod (depends on OS/Docker; **Docker Desktop** often supports `host.docker.internal`).
   - **Linux:** use the host gateway IP from the node network or route traffic via a **NodePort** / **LoadBalancer** Service that targets the Jaeger process.

For debugging, set `tracing.samplingRatio` to `1.0` in Kueue `Configuration`, apply the change, restart or roll the controller if needed, then run a focused package under [`test/e2e`](https://github.com/kubernetes-sigs/kueue/tree/main/test/e2e). In Jaeger, look for the service name **`kueue`** (set by the controller at startup).

Verification checklist:

1. Jaeger UI loads and OTLP ports are listening.
2. Kueue `Configuration` has `tracing.enable: true` and the controller pod has the intended `OTEL_*` env vars (`kubectl describe pod -n <namespace> …`).
3. Run an e2e spec that admits Workloads and confirm spans in Jaeger.
