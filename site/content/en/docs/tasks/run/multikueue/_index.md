---
title: "Using MultiKueue"
linkTitle: "MultiKueue"
weight: 8
date: 2024-09-25
description: >
  The workloads that support MultiKueue.
---

This page explains how to run tasks in MultiKueue environment.

For more details about the MultiKueue check the concepts section for a [MultiKueue overview](/docs/concepts/multikueue/). 

## Running tasks in MultiKueue environment

To utilize this feature, you simply submit the job to the Manager cluster, targeting a ClusterQueue that has been configured for MultiKueue operation.

Once submitted, Kueue automatically handles the delegation of the job to the appropriate worker clusters, requiring no additional configuration changes on your part.