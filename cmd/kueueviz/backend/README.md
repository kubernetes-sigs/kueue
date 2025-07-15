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
# Start with default value
./kueue_ws_app

# Start on port 8181
KUEUE_VIZ_PORT=8181 ./kueue_ws_app
```

## Variables

| Environment variables | Description              | Default value |
| --------------------- | ------------------------ | ------------- |
| `KUEUE_VIZ_PORT`      | Default application port | 8080          |
| `GIN_MODE`            | Gin mode                 | debug         |

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
