---
title: "kubectl kueue list"
linkTitle: "List"
date: 2024-05-10
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
# List workloads
kubectl kueue list localqueue my-local-queue
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name       | Short | API version            | Namespaced | Kind       |
|------------|-------|------------------------|------------|------------|
| localqueue | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue |