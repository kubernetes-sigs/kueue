# Kueue WebSocket Application

## Description

This Go application provides WebSocket endpoints for interacting with Kueue resources in a Kubernetes cluster. It uses the Gin framework for HTTP and WebSocket handling and the Kubernetes Go client for API interactions.

## Features

- Fetch and broadcast `localqueues` over WebSocket.

## Prerequisites

- A Kubernetes cluster
- Go 1.19+
- `kubectl` configured to access the cluster

## Installation

1. Clone this repository.
2. Ensure Go is installed on your machine.

## Build

Run the following command to build the application:

```bash
CGO_ENABLED=0 go build -o kueue_ws_app
```

## Run

Run the application:

```bash
# Start with default value (listen on :8080)
./kueue_ws_app

# Start on a custom listen address (host:port)
./kueue_ws_app --listen=0.0.0.0:8181

# Equivalent using environment variables
KUEUEVIZ_LISTEN=0.0.0.0:8181 ./kueue_ws_app

# Port-only (legacy / backward compatible)
./kueue_ws_app --port=8181
KUEUEVIZ_PORT=8181 ./kueue_ws_app

# Adjust log verbosity
./kueue_ws_app --log-level=debug
KUEUEVIZ_LOG_LEVEL=debug ./kueue_ws_app
```

## Flags and variables

CLI flags take precedence over environment variables.

| Flag          | Environment variable     | Description                                      | Default value  |
| ------------- | ------------------------ | ------------------------------------------------ | -------------- |
| `--listen`    | `KUEUEVIZ_LISTEN`        | Listen address (`host:port`), e.g. `0.0.0.0:8181` | _(use --port)_ |
| `--port`      | `KUEUEVIZ_PORT`          | TCP port when `--listen` is unset                | `8080`         |
| `--log-level` | `KUEUEVIZ_LOG_LEVEL`     | Log level: `debug`, `info`, `warn`, `error`      | `info`         |
|               | `GIN_MODE`               | Gin mode                                         | `debug`        |
|               | `KUEUEVIZ_ALLOWED_ORIGINS` | Comma-separated list of CORS origins           | `*` (dev only) |

`--listen` is preferred when co-locating the frontend and backend in the same pod so each container can bind a distinct address/port.

## Endpoints

### WebSocket

## WebSocket Endpoints

| Endpoint                                          | Description                             |
| ------------------------------------------------- | --------------------------------------- |
| `/ws/local-queues`                                | Streams updates for local queues        |
| `/ws/cluster-queues`                              | Streams updates for cluster queues      |
| `/ws/workloads`                                   | Streams updates for workloads           |
| `/ws/resource-flavors`                            | Streams updates for resource flavors    |
| `/ws/resource-flavor/{flavor_name}`               | Streams updates for a specific flavor   |
| `/ws/local-queue/{namespace}/{queue_name}`        | Streams updates for a specific queue    |
| `/ws/cohorts`                                     | Streams updates for cohorts             |
| `/ws/cohort/{cohort_name}`                        | Streams updates for a specific cohort   |
| `/ws/workload/{namespace}/{workload_name}`        | Streams updates for a specific workload |
| `/ws/workload/{namespace}/{workload_name}/events` | Streams events for a specific workload  |

### REST API

| Endpoint                                          | Description                             |
| ------------------------------------------------- | --------------------------------------- |
| `/api/{resource_type}/{name}?namespace={namespace}&output={output_type}` | Returns content for a specific resource and output type |
