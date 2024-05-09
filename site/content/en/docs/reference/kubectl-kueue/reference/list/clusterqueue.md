---
title: "kubectl kueue list clusterqueue"
linkTitle: "clusterqueue"
date: 2024-05-09
weight: 10
description: >
  Lists all ClusterQueues, potentially limiting output to those that are active/inactive and matching the label selector.
---

### Usage:

```
kubectl kueue list clusterqueue [--selector key1=value1] [--field-selector key1=value1] [--active=*|true|false]
```

### Examples:

```bash
kubectl kueue list clusterqueue
```

### Options:

```
  -o, --output string           Output format. One of: (json, yaml, name, go-template, go-template-file, template, templatefile, jsonpath, jsonpath-as-json, jsonpath-file).
  -l, --selector string         Selector (label query) to filter on, supports '=', '==', and '!='.(e.g. -l key1=value1,key2=value2). Matching objects must satisfy all of the specified label constraints.
      --field-selector string   Selector (field query) to filter on, supports '=', '==', and '!='.(e.g. --field-selector key1=value1,key2=value2). The server only supports a limited number of field queries per type.
      --active string           Filter by active status of cluster queues. Valid values are '*', 'true', 'false'.
```

You can run `kubectl kueue list clusterqueue --help` in the terminal to get all possible flags.