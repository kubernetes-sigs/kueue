---
title: "OpenTelemetry 链路追踪"
linkTitle: "链路追踪"
date: 2026-04-29
description: >
  使用 OpenTelemetry OTLP 为 Workload 控制器导出的可选分布式追踪。
---

Kueue 可在 `Configuration` 中开启对 [Workload](/zh-cn/docs/concepts/workload) 控制器的 **OpenTelemetry** 追踪。

```yaml
tracing:
  enable: true
  samplingRatio: 0.01   # 可选，0.0–1.0，默认 0.01
```

使用标准环境变量配置 OTLP gRPC 导出，例如 `OTEL_EXPORTER_OTLP_ENDPOINT`、`OTEL_EXPORTER_OTLP_INSECURE`。

Workload 上的注解 `kueue.x-k8s.io/trace-context` 用于在多次 reconcile 之间延续同一 trace；与 Kubernetes **Events** 无关。

## 本地 Jaeger（开发）

仓库内提供 Compose 示例：

```bash
docker compose -f hack/tracing/docker-compose.yaml up -d
```

- UI：`http://localhost:16686`
- OTLP gRPC：`4317`

在**本机直接运行** Kueue 二进制时，可将 `OTEL_EXPORTER_OTLP_ENDPOINT` 设为 `127.0.0.1:4317`，并设置 `OTEL_EXPORTER_OTLP_INSECURE=true`。说明见 [`hack/tracing/README.md`](https://github.com/kubernetes-sigs/kueue/blob/main/hack/tracing/README.md)。

## 端到端测试与集群网络

e2e 针对已安装 Kueue 的真实集群；追踪仍通过 Kueue `Configuration` 与控制器 Pod 上的 `OTEL_*` 环境变量配置。**Pod 内** `127.0.0.1` 指向本 Pod，不是宿主机。可选做法：在集群内部署 Jaeger/Collector 并使用 Service DNS；或在宿主机跑 Jaeger，再通过 kind 的 `extraPortMappings` / `host.docker.internal`（视环境而定）等让 Pod 能访问宿主机上的 OTLP 端口。

调试可将 `tracing.samplingRatio` 设为 `1.0`，运行 [`test/e2e`](https://github.com/kubernetes-sigs/kueue/tree/main/test/e2e) 下用例后在 Jaeger 中筛选服务名 **`kueue`**。

完整表格与示例（英文）见 [OpenTelemetry tracing](/docs/reference/tracing/)。
