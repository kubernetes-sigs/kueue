---
title: "kubectl kueue create"
linkTitle: "Create"
date: 2024-05-10
weight: 20
description: >
  Create a resource
---

### Usage:

```
kubectl kueue create [TYPE]
```

### Examples:

```bash
# Create a local queue
kubectl kueue create localqueue my-local-queue -c my-cluster-queue

# Create a local queue with unknown cluster queue
kubectl kueue create localqueue my-local-queue -c my-cluster-queue -i
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name       | Short | API version            | Namespaced | Kind       |
|------------|-------|------------------------|------------|------------|
| localqueue | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue |