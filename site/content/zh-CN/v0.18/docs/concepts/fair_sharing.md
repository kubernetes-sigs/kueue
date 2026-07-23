---
title: "公平共享（Fair Sharing）"
date: 2025-05-28
weight: 6
description: >
  Kueue 中的机制，可在租户之间公平地分配配额。
---

### [准入公平共享](/zh-CN/docs/concepts/admission_fair_sharing)

一种根据源 LocalQueue 的历史资源使用情况对工作负载进行排序的机制，
优先考虑那些随着时间的推移消耗较少资源的工作负载。

### [基于抢占的公平共享](/zh-CN/docs/concepts/preemption/#fair-sharing)
