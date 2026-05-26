---
title: "Configure external cert-manager"
date: 2025-03-14
weight: 1
description: >
  Cert Manager Support
---

This page shows how you can a third party certificate authority solution like
Cert Manager.

The page is intended for a [batch administrator](/docs/tasks#batch-administrator).

## Before you begin

Make sure you the following conditions are set:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation).
- Cert Manager is [installed](https://cert-manager.io/docs/installation/)

Kueue supports either Kustomize or installation via a Helm chart.

### Internal Certificate management

In all cases, Kueue's internal certificate management must be turned off
if one wants to use CertManager.

### Kustomize Installation

  1. Set `internalCertManagement.enable` to `false` in the kueue configuration.
  2. Comment out the `internalcert` folder in `config/default/kustomization.yaml`.
  3. Enable `cert-manager` in `config/default/kustomization.yaml` and uncomment all sections with 'CERTMANAGER'.

### Helm Installation

Kueue also supports Cert Manager integration through Helm values.
When `enableCertManager` is set to `true`, the chart automatically disables
Kueue's internal certificate management in the generated configuration.

1. Set `enableCertManager` to `true` in your `values.yaml` file.
2. By default, the chart creates a self-signed `Issuer`.
3. To reuse an existing `Issuer` or `ClusterIssuer`, set `certManager.issuerRef`.
4. If you reference a namespace-scoped `Issuer`, it must already exist in the
   same namespace as the Helm release.
5. The referenced issuer must provide the CA data required by Kueue's
   cert-manager integration, including `ca.crt` in the generated Secrets and
   the CA bundle used for webhook and visibility API injection.

For example, to use an existing `ClusterIssuer`:

```yaml
enableCertManager: true
certManager:
  issuerRef:
    group: cert-manager.io
    kind: ClusterIssuer
    name: my-cluster-issuer
```
