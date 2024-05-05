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
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name     | Short | API version            | Namespaced | Kind     |
|----------|-------|------------------------|------------|----------|
| workload | wl    | kueue.x-k8s.io/v1beta1 | true       | Workload |