---
title: "Configure Prometheus with TLS"
date: 2025-03-14
weight: 2
description: >
  Configure Prometheus with TLS using cert-manager
---

{{% alert title="Note" color="primary" %}}
For basic Prometheus setup without TLS, see [Setup Prometheus](/docs/tasks/manage/observability/setup_prometheus).
This page covers advanced TLS configuration with cert-manager.
{{% /alert %}}

This page shows how to configure Kueue to use Prometheus metrics with TLS encryption.

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- Prometheus is [installed](https://prometheus-operator.dev/docs/getting-started/installation/)
- Cert Manager can be optionally [installed](https://cert-manager.io/docs/installation/)

Kueue supports either Kustomize or installation via a Helm chart.

### Kustomize Installation

1. Enable `prometheus` in `config/default/kustomization.yaml` and uncomment all sections with 'PROMETHEUS'.

#### Kustomize Prometheus with certificates

If you want to enable TLS verification for the metrics endpoint, follow the directions below.

  1. Set `internalCertManagement.enable` to `false` in the kueue configuration.
  2. Comment out the `internalcert` folder in `config/default/kustomization.yaml`.
  3. Enable `cert-manager` in `config/default/kustomization.yaml` and uncomment all sections with 'CERTMANAGER'.
  4. To enable secure metrics with TLS protection, uncomment all sections with 'PROMETHEUS-WITH-CERTS'.

### Helm Installation

#### Prometheus installation

Kueue can also supports helm deployment for Prometheus.

1. Set `enablePrometheus` in your values.yaml file to true.

#### Helm Prometheus with certificates

If you want to secure the metrics endpoints with external certificates:

1. Disable internal cert management in the kueue configuration (see [custom-configuration](https://kueue.sigs.k8s.io/docs/installation/#install-a-custom-configured-released-version) for more details).
2. Set both `enableCertManager` and `enablePrometheus` to true.
3. Provide values for the tlsConfig, see the example below:

An example for your tlsConfig in the helm chart could be as follows:

```yaml
...
metrics:
  prometheusNamespace: monitoring
# tls configs for serviceMonitor
  serviceMonitor:
    tlsConfig:
      serverName: kueue-controller-manager-metrics-service.kueue-system.svc
      ca:
        secret:
          name: kueue-metrics-server-cert
          key: ca.crt
      cert:
        secret:
          name: kueue-metrics-server-cert
          key: tls.crt
      keySecret:
        name: kueue-metrics-server-cert
        key: tls.key
```

The secrets must reference the cert manager generated secrets.
