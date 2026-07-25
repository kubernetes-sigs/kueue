---
title: "Ecosystem Resources"
linkTitle: "Ecosystem Resources"
weight: 7
date: 2026-07-15
description: >
  A curated list of vendor-maintained documentation, tutorials, blogs, and repositories about running Kueue.
---

Many cloud providers, hardware vendors, and platform builders maintain their own Kueue
documentation, tutorials, and reference implementations, and some ship Kueue as
a built-in component of their AI/HPC stacks.

This page collects those materials in one place, so that you can find the
guidance relevant to your environment.

{{% alert title="Note" color="primary" %}}
The resources below are owned and maintained by the respective vendors, not by
the Kueue project. They may target specific Kueue versions or
vendor-customized builds, and can drift from the upstream documentation.
Always cross-check version compatibility with the [official docs](/v0.19/docs/) and
the vendor's release notes. Listing here is not an endorsement; the order is
alphabetical and the list is not exhaustive — contributions adding further
materials are welcome.
{{% /alert %}}

## Amazon Web Services (EKS, SageMaker HyperPod)

Amazon SageMaker HyperPod *task governance* is built on top of Kueue: it
manages quotas, priorities, preemption, and gang scheduling for EKS-based
HyperPod GPU clusters through Kueue APIs.

Documentation:

- [SageMaker HyperPod task governance](https://docs.aws.amazon.com/sagemaker/latest/dg/sagemaker-hyperpod-eks-operate-console-ui-governance.html) — overview and setup of the Kueue-based governance layer.
- [Using gang scheduling in Amazon SageMaker HyperPod task governance](https://docs.aws.amazon.com/sagemaker/latest/dg/sagemaker-hyperpod-eks-operate-console-ui-governance-tasks-gang-scheduling.html) — all-or-nothing admission for distributed training.
- [Using topology-aware scheduling in Amazon SageMaker HyperPod task governance](https://docs.aws.amazon.com/sagemaker/latest/dg/sagemaker-hyperpod-eks-operate-console-ui-governance-tasks-scheduling.html) — network-topology-based placement.


Blogs and repositories:

- [Best practices for Amazon SageMaker HyperPod task governance](https://aws.amazon.com/blogs/machine-learning/best-practices-for-amazon-sagemaker-hyperpod-task-governance/) — quota design and team sharing patterns.
- [Maximize HyperPod Cluster utilization with HyperPod task governance fine-grained quota allocation](https://aws.amazon.com/blogs/machine-learning/maximize-hyperpod-cluster-utilization-with-hyperpod-task-governance-fine-grained-quota-allocation/).
- [Schedule topology-aware workloads using Amazon SageMaker HyperPod task governance](https://aws.amazon.com/blogs/machine-learning/schedule-topology-aware-workloads-using-amazon-sagemaker-hyperpod-task-governance/).
- [Introducing AI on EKS: powering scalable AI workloads with Amazon EKS](https://aws.amazon.com/blogs/containers/introducing-ai-on-eks-powering-scalable-ai-workloads-with-amazon-eks/) — AWS open-source initiative for AI on EKS, including Kueue-based scheduling blueprints.
- [MLOps on Amazon EKS - Comprehensive Guide](https://github.com/aws-samples/amazon-eks-machine-learning-with-terraform-and-kubeflow) — MLOps reference stack on EKS with Kueue as a modular component.

## CoreWeave (CKS)

- [Kueue on CoreWeave Kubernetes Service](https://docs.coreweave.com/products/cks/clusters/coreweave-charts/kueue) — CoreWeave-maintained Helm chart, with metrics automatically ingested into a Kueue Grafana dashboard.
- [Kueue: A Kubernetes-native System for AI Training Workloads](https://www.coreweave.com/blog/kueue-a-kubernetes-native-system-for-ai-training-workloads) — how AI labs use Kueue on CKS for training and batch inference.

## Google Cloud (GKE, AI Hypercomputer)

Documentation:

- [Deploy a batch system using Kueue](https://cloud.google.com/kubernetes-engine/docs/tutorials/kueue-intro) — the canonical GKE Kueue starter tutorial (two tenant teams sharing a cluster).
- [Implement a Job queuing system with quota sharing between namespaces on GKE](https://cloud.google.com/kubernetes-engine/docs/tutorials/kueue-cohort) — cohorts and quota borrowing on GKE.
- [Best practices for running batch workloads on GKE](https://docs.cloud.google.com/kubernetes-engine/docs/best-practices/batch-platform-on-gke) — platform-level guidance with Kueue at the center of the queueing story.
- [Best practices for GKE AI/ML workload prioritization](https://docs.cloud.google.com/kubernetes-engine/docs/best-practices/optimize-ai-utilization) — priorities and preemption for AI workloads.
- [Orchestrate Multislice workloads using JobSet and Kueue](https://docs.cloud.google.com/kubernetes-engine/docs/tutorials/tpu-multislice-kueue) — TPU Multislice with JobSet + Kueue.
- [Run a large-scale workload with flex-start with queued provisioning](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/provisioningrequest) — Kueue-managed ProvisioningRequests on GKE.
- [Optimize AI training on TPUs with DWS, Ray and Kueue](https://docs.cloud.google.com/kubernetes-engine/docs/tutorials/ray-kueue-dws).
- [Optimize GKE resource utilization for mixed AI/ML training and inference workloads](https://docs.cloud.google.com/kubernetes-engine/docs/tutorials/mixed-workloads)
- [Schedule GKE workloads with Topology Aware Scheduling](https://docs.cloud.google.com/ai-hypercomputer/docs/workloads/schedule-gke-workloads-tas) — Kueue TAS on AI-optimized GKE clusters.
- [Create an AI-optimized GKE cluster](https://docs.cloud.google.com/ai-hypercomputer/docs/create/gke-ai-hypercompute) — AI Hypercomputer cluster creation, with Kueue in the workload-scheduling toolchain.

Blogs and repositories:

- [Google Cluster Toolkit (formerly HPC Toolkit)](https://github.com/GoogleCloudPlatform/cluster-toolkit) — blueprints for AI/HPC clusters on Google Cloud, including Kueue with TAS.
- [GKE AI Labs](https://gke-ai-labs.dev/).
- [With MultiKueue, grab GPUs for your GKE cluster, wherever they may be](https://cloud.google.com/blog/products/containers-kubernetes/using-multikueue-to-provision-global-gpu-resources) — using MultiKueue and DWS across many Google Cloud regions.
- [MultiKueue with ClusterProfile API and GKE Fleet](https://github.com/GoogleCloudPlatform/gke-fleet-management/blob/b9fe08386c48f84617cb8ab7b042f2790741e893/multikueue-clusterprofile/README.md) — end-to-end walkthrough of using MultiKueue with GKE Fleet.
- [NeMo Framework on Google Kubernetes Engine (GKE) to train Megatron LM](https://github.com/GoogleCloudPlatform/nvidia-nemo-on-gke) 

## IBM

- [MLBatch](https://github.com/project-codeflare/mlbatch) — queueing and quota management setup for AI/ML batch jobs, combining Kueue, AppWrapper, Kubeflow Training Operator and KubeRay, with detailed operational best practices.
- [AppWrapper](https://github.com/project-codeflare/appwrapper) — the AppWrapper controller for Kueue.
- [Improve GPU utilization with Kueue in OpenShift AI. How IBM achieved 90% GPU allocation in Vela](https://developers.redhat.com/articles/2025/05/22/improve-gpu-utilization-kueue-openshift-ai).

## Microsoft Azure (AKS)
- [Install and Configure Kueue on Azure Kubernetes Service (AKS)](https://learn.microsoft.com/en-us/azure/aks/kueue-overview) — AKS-maintained overview and installation guide.
- [Schedule and deploy batch jobs with Kueue on Azure Kubernetes Service (AKS)](https://learn.microsoft.com/en-us/azure/aks/deploy-batch-jobs-with-kueue) — ResourceFlavors, ClusterQueues and sample batch jobs on AKS.
- [https://blog.aks.azure.com/2025/12/05/kubernetes-ai-conformance-aks](https://blog.aks.azure.com/2025/12/05/kubernetes-ai-conformance-aks) — AKS engineering blog positioning Kueue within the Kubernetes AI conformance stack.

## Oracle Cloud Infrastructure (OKE)
- [Running RDMA (Remote Direct Memory Access) GPU Workloads on OKE](https://github.com/oracle-quickstart/oci-hpc-oke) — Oracle's reference stack for GPU clusters on OKE; recent versions deploy Kueue (with Topology, ResourceFlavor, ClusterQueue, and LocalQueue objects) by default.
- [Using RDMA Network Locality When Running Workloads on OKE](https://github.com/oracle-quickstart/oci-hpc-oke/blob/main/docs/using-rdma-network-locality-when-running-workloads-on-oke.md).

## RedHat

- [Red Hat OpenShift is joining the Kueue](https://www.redhat.com/en/blog/openshift-joining-kueue) — announcement blog.
- [Red Hat build of Kueue documentation](https://docs.redhat.com/en/documentation/openshift_container_platform/4.20/html/ai_workloads/red-hat-build-of-kueue).
- [Improve GPU utilization with Kueue in OpenShift AI. How IBM achieved 90% GPU allocation in Vela](https://developers.redhat.com/articles/2025/05/22/improve-gpu-utilization-kueue-openshift-ai).

## Other external docs

- [Ray: Gang scheduling, Priority scheduling, and Autoscaling for KubeRay CRDs with Kueue](https://docs.ray.io/en/latest/cluster/kubernetes/k8s-ecosystem/kueue.html).
- [LeaderWorkerSet: Topology Aware Scheduling with Kueue](https://lws.sigs.k8s.io/docs/examples/tas/).
