---
title: "Enabling pprof endpoints"
date: 2023-07-21
weight: 3
description: >
  Enable pprof endpoints for Kueue controller manager.
---

This page shows you how to enable pprof endpoints for Kueue controller manager.

The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).

## Enabling pprof endpoints

{{< feature-state state="stable" for_version="v0.5" >}}

To enable pprof endpoints, you need to set a `pprofBindAddress` is set in the [manager's configuration](/docs/installation/#install-a-custom-configured-released-version).

The easiest way to reach pprof port in kubernetes is to use `port-forward` command.

1. Run the following command to obtain the name of the Pod running Kueue:

```shell
kubectl get pod -n kueue-system
NAME                                        READY   STATUS    RESTARTS   AGE
kueue-controller-manager-769f96b5dc-87sf2   2/2     Running   0          45s
```

2. Run the following command to initiate the port forwarding to your localhost:

```shell
kubectl port-forward kueue-controller-manager-769f96b5dc-87sf2 -n kueue-system 8083:8083
Forwarding from 127.0.0.1:8083 -> 8083
Forwarding from [::1]:8083 -> 8083
```

The HTTP endpoint will now be available as a local port.

To learn how to use the exposed endpoint, see [pprof basic usage](https://github.com/google/pprof#basic-usage) and [examples](https://pkg.go.dev/net/http/pprof#hdr-Usage_examples).
