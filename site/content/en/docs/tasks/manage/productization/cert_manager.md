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

Kueue can also support optional helm values for Cert Manager enablement.

1. Disable `internalCertManager` in the kueue configuration.
2. set `enableCertManager` in your values.yaml file to true.
