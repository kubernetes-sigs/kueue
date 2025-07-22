---
title: "Enable KueueViz"
date: 2025-07-18
weight: 12
description: >
  Installing and configuring KueueViz, a web-based visualization tool for Kueue workload monitoring.
---

{{% alert title="Note" color="primary" %}}
For a streamlined installation and simplified upgrades, we recommend deploying Kueue using Helm.  
You can customize the deployment with a local `values.yaml` file to fit your environment.  
See the [Helm chart installation guide](/docs/installation/#install-by-helm) for full instructions.
{{% /alert %}}

KueueViz is a web-based visualization tool that provides real-time monitoring of Kueue workloads, queues, and resource allocation. It offers an intuitive dashboard for observing job queue status, resource utilization, and workload progression.

This page shows how to install and configure KueueViz in your cluster.

The page is intended for [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running
- The kubectl command-line tool has communication with your cluster
- The Helm command-line tool is installed
- Kueue is installed in your cluster
- (Optional) An ingress controller for external access (e.g. Nginx Ingress Controller)

KueueViz can be installed using Helm (recommended), or kubectl. Choose the method that best fits your workflow.

## Installation with Kueue

To install KueueViz as part of a new Kueue installation:

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --create-namespace \
  --set enableKueueViz=true \ # enable KueueViz
  --wait --timeout 300s
```

For more information on installing Kueue, please refer to [Installation](/docs/installation).

## Enable KueueViz (Kueue is already installed)

If Kueue is already installed, you can enable KueueViz by Helm or kubectl.

### Enable KueueViz by Helm

To enable KueueViz on an existing Kueue installation by Helm:

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --set enableKueueViz=true # enable KueueViz
```

### Enable KueueViz by YAML

To enable KueueViz on an existing Kueue installation by YAML:

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## Accessing the Dashboard

### Port Forwarding (Only for development)

For quick access during development or testing(had tested by Docker Desktop):

```bash
kubectl port-forward svc/kueue-kueue-viz-frontend -n kueue-system 8080
kubectl port-forward svc/kueue-kueue-viz-backend  -n kueue-system 8081:8080
```

Edit the kueue-viz-frontend Deployment to set env `REACT_APP_WEBSOCKET_URL=ws://localhost:8081`.

Then access the dashboard at [http://localhost:8080](http://localhost:8080).

### Ingress

For production deployments, configure an Ingress resource:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kueueviz-ingress
  namespace: kueue-system
spec:
  rules:
    - host: kueueviz.example.com
      http:
        paths:
          - path: /api(/|$)(.*)
            pathType: Prefix
            backend:
              service:
                name: kueue-kueueviz-backend
                port:
                  number: 8080
          - path: /
            pathType: Prefix
            backend:
              service:
                name: kueue-kueueviz-frontend
                port:
                  number: 8080
  tls:
    - hosts:
        - kueueviz.example.com # replace with your domain
      secretName: kueueviz-tls # you need to create a TLS secret at first
```

### LoadBalancer

For cloud environments with LoadBalancer support:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: kueueviz-loadbalancer
  namespace: kueue-system
spec:
  type: LoadBalancer
  ports:
    - name: http
      port: 80
      targetPort: 8080
      protocol: TCP
  selector:
    app.kubernetes.io/name: kueue
    app.kubernetes.io/component: kueueviz-frontend
```

## Upgrade

### Upgrade by Helm

To upgrade KueueViz by Helm:

```bash
helm upgrade kueue oci://registry.k8s.io/kueue/charts/kueue \
  --version={{< param "chart_version" >}} \
  --namespace kueue-system \
  --set enableKueueViz=true
```

### Upgrade by YAML

To upgrade KueueViz by YAML:

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## Uninstall

**Note:** Be sure to uninstall KueueViz, and not to accidentally uninstall Kueue instead.

To uninstall KueueViz components:

```bash
kubectl delete -f https://github.com/kubernetes-sigs/kueue/releases/download/{{< param "chart_version" >}}/kueueviz.yaml
```

## What's next

- Explore more [Tasks](/docs/tasks)
- Learn about [Concepts](/docs/concepts)
