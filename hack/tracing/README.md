# Local Jaeger for Kueue tracing

Start a [Jaeger](https://www.jaegertracing.io/) all-in-one that accepts **OTLP gRPC** on port **4317** and serves the UI on **16686**.

## Run

```sh
docker compose -f hack/tracing/docker-compose.yaml up -d
```

- UI: <http://localhost:16686>
- Stop: `docker compose -f hack/tracing/docker-compose.yaml down`

## Point Kueue at this instance

Kueue must send OTLP to an address it can reach (not `127.0.0.1` from inside a pod unless you use host networking). See [OpenTelemetry tracing](https://kueue.sigs.k8s.io/docs/reference/tracing/) in the docs (*Local Jaeger* and *End-to-end tests and cluster networking*).

## Optional: OpenTelemetry Collector

The Jaeger all-in-one image already ingests OTLP. Add a separate Collector only if you need processor pipelines (batch, attributes) matching production. A minimal `otel-collector` in front of Jaeger is a common production pattern; for local viewing, direct export to Jaeger is enough.
