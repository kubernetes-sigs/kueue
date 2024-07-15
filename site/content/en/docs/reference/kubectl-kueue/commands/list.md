---
title: "kubectl kueue list"
linkTitle: "List"
date: 2024-07-03
weight: 10
description: >
  List resource
---

### Usage:

```
kubectl kueue list [TYPE]
```

### Examples:

```bash
# List cluster queues
kubectl kueue list clusterqueue

# List local queues
kubectl kueue list localqueue

# List workloads
kubectl kueue list workload

# List resource flavors
kubectl kueue list resourceflavor
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name           | Short | API version            | Namespaced | Kind           |
|----------------|-------|------------------------|------------|----------------|
| localqueue     | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue     |
| clusterqueue   | cq    | kueue.x-k8s.io/v1beta1 | false      | ClusterQueue   |
| workload       | wl    | kueue.x-k8s.io/v1beta1 | true       | WorkLoad       |
| resourceflavor | wl    | kueue.x-k8s.io/v1beta1 | false      | ResourceFlavor |
