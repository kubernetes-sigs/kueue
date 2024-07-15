---
title: "kubectl kueue resume"
linkTitle: "Resume"
date: 2024-05-10
weight: 30
description: >
  Resume the resource
---

### Usage:

```
kubectl kueue resume [TYPE]
```

### Examples:

```bash
# Resume the workload 
kubectl kueue resume workload my-workload
# Resume the localqueue
kubectl kueue resume localqueue my-localqueue
# Resume the ClusterQueue 
kubectl kueue resume clusterqueue my-clusterqueue
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name     | Short | API version            | Namespaced | Kind     |
|----------|-------|------------------------|------------|----------|
| workload | wl    | kueue.x-k8s.io/v1beta1 | true       | Workload |
| localqueue | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue |
| clusterqueue | cq    | kueue.x-k8s.io/v1beta1 | false       | ClusterQueue |
