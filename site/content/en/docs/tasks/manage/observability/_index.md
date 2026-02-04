---
title: "Observability"
linkTitle: "Observability"
weight: 5
date: 2026-02-04
description: >
  Monitor Kueue with Prometheus metrics
---

Kueue exposes Prometheus metrics to monitor the health of the system and the status of ClusterQueues and LocalQueues.

## In this section

- [Setup Prometheus](setup_prometheus) - Configure Prometheus to scrape Kueue metrics

## Related

- [Prometheus Metrics Reference](/docs/reference/metrics) - Complete list of available metrics
- [Monitor pending Workloads](/docs/tasks/manage/monitor_pending_workloads/) - Query pending workloads using the Visibility API
- [Configure Prometheus with TLS](/docs/tasks/manage/productization/prometheus) - Advanced TLS configuration with cert-manager
- [Setup Dev Monitoring](/docs/tasks/dev/setup_dev_monitoring) - Developer guide for local monitoring setup
