---
title: "kubectl kueue stop"
linkTitle: "Stop"
date: 2024-05-10
weight: 40
description: >
  Stop the resource
---

### Usage:

```
kubectl kueue stop [TYPE]
```

### Examples:
```bash
# Stop the workload
kubectl kueue stop workload my-workload
# Stop the localqueue
kubectl kueue stop localqueue my-localqueue
# Stop the ClusterQueue
kubectl kueue stop clusterqueue my-clusterqueue
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name     | Short | API version            | Namespaced | Kind     |
|----------|-------|------------------------|------------|----------|
| workload | wl    | kueue.x-k8s.io/v1beta1 | true       | Workload |
| localqueue | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue |
| clusterqueue | cq    | kueue.x-k8s.io/v1beta1 | false       | ClusterQueue |
