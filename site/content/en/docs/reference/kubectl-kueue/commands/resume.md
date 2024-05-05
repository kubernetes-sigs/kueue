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
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name     | Short | API version            | Namespaced | Kind     |
|----------|-------|------------------------|------------|----------|
| workload | wl    | kueue.x-k8s.io/v1beta1 | true       | Workload |