---
title: "采用者"
linkTitle: "采用者"
weight: 30
menu:
  main:
    weight: 30
description: >
  Kueue 的使用场景及使用方式
aliases:
- /adopters
---

以下是在生产环境中使用了 Kueue 的各个组织列表及其使用的集成组件。
如果你正在使用 Kueue，可以通过打开一个 PR 来添加你的组织到此列表。

## 采用者

|                      组织                       |   类型   |                           描述                           |             集成              |                     联系                      |
|:------------------------------------------------:|:--------:|:-----------------------------------------------------:|:-----------------------------:|:----------------------------------------------:|
| [CyberAgent, Inc.](https://www.cyberagent.co.jp/en/)    | 最终用户 |                企业内部 ML 平台                   |  batch/job </br> kubeflow.org/mpijob  |    [@tenzen-y](https://github.com/tenzen-y)      |
|      [DaoCloud, Inc.](https://www.daocloud.io/en/)      | 最终用户 |        AI 平台的一部分，用于管理各类 Jobs。          |   batch/job </br> RayJob </br> ...    |     [@kerthcet](https://github.com/kerthcet)     |
|            [WattIQ, Inc.](https://wattiq.io)            | 最终用户 |                 SaaS/IoT 产品                    |     batch/job </br> RayJob </br>      | [@madsenwattiq](https://github.com/madsenwattiq) |
|          [Horizon, Inc.](https://horizon.cc/)           | 最终用户 |                  AI 训练平台                      |         batch/job </br> ...           |      [@GhangZh](https://github.com/GhangZh)      |
|                [FAR AI](https://far.ai/)                | 最终用户 |                 AI 对齐研究非营利组织               |               batch/job               |     [@rhaps0dy](https://github.com/rhaps0dy)     |
|           [Shopee, Inc.](https://shopee.com/)           | 最终用户 | 在 AI 平台测试环境中进行训练/batch 推理/数据处理       | 自定义 job </br> RayJob </br> ... |     [@denkensk](https://github.com/denkensk)     |
|           [Mondoo, Inc.](https://mondoo.com)            | 最终用户 |          为 Mondoo 的托管安全扫描器提供支持           |               batch/job               |         [@jaym](https://github.com/jaym)         |
|        [Google Cloud](https://cloud.google.com/)        | 提供商   | 是[用于在 TPU 上训练 ML 工作负载的套件][gcmldemo] 的一部分 |                JobSet                 |     [@mrozacki](https://github.com/mrozacki)     |
|       [Onna Technologies, Inc](https://onna.com)        | 最终用户 |              非结构化数据管理平台                      |            batch/job </br>            |     [@gitcarbs](https://github.com/gitcarbs)     |
|       [IBM Research](https://research.ibm.com)          | 最终用户 | 作为运行 AI/ML 工作负载的 [MLBatch][mlbatch] 栈的一部分 | AppWrapper</br>PyTorchJob</br>RayJob  |    [dgrove-oss](https://github.com/dgrove-oss)   |
|       [Innovatrics](https://www.innovatrics.com/)       | 最终用户 |                     企业内部 ML 平台                  |          batch/job </br> pod          |    [@mmolisch](https://github.com/mmolisch)      |
| [Red Hat, Inc.](https://www.redhat.com/en)              | 最终用户 | 云/企业内部 ML 平台 | Ray Cluster <br> RayJob <br> Pytorch Job <br> TensorFlow Job <br> ... | [@varshaprasad96](https://github.com/varshaprasad96) |
| [Octue](https://octue.com)                              | 最终用户 | [科学数据服务框架][octue-sdk]                  |               batch/job               | [@cortadocodes](https://github.com/cortadocodes) |

[gcmldemo]: https://cloud.google.com/blog/products/compute/the-worlds-largest-distributed-llm-training-job-on-tpu-v5e
[mlbatch]: https://github.com/project-codeflare/mlbatch
[octue-sdk]: https://github.com/octue/octue-sdk-python
