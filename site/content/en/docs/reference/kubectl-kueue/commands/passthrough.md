---
title: "Pass-through commands"
linkTitle: "Pass-through"
date: 2024-05-14
weight: 100
description: >
  Commands delegated to `kubectl`
---

### Usage:

```
kubectl kueue <command> <type> [FLAGS]
```

### Examples:

```bash
# Edit a local queue
kubectl kueue edit localqueue my-local-queue

# Delete a cluster queue
kubectl kueue delete cq my-cluster-queue
```

## Supported commands

The following table includes a list of commands that are passed through to `kubectl`.

| Name       | Short                                                                                            |
|------------|--------------------------------------------------------------------------------------------------|
| get        |  Display one or many resources
| describe   |  Show details of a specific resource or group of resources
| edit       |  Edit a resource on the server
| patch      |  Update fields of a resource
| delete     |  Delete resources by file names, stdin, resources and names, or by resources and label selector

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name         | Short | API version            | Namespaced | Kind         |
|--------------|-------|------------------------|------------|--------------|
| workload     | wl    | kueue.x-k8s.io/v1beta1 | true       | Workload     |
| clusterqueue | cq    | kueue.x-k8s.io/v1beta1 | false      | ClusterQueue |
| localqueue   | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue   |
