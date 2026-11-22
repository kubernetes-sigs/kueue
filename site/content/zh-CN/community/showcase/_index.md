---
title: "采用者、演讲与社区资源"
linkTitle: "采用者与资源"
description: "探索在生产环境中运行 Kueue 的组织、会议演讲、技术教程以及来自社区的最新工程文章。"
weight: 31
menu:
  main:
    weight: 31
---

<!-- 1. SHOWCASE PILLARS -->
<div class="row g-4 mb-5">
  <!-- Official Adopters -->
  <div class="col-md-6">
    <div class="card h-100 feature-card bg-light border-0 rounded-3 p-3">
      <div class="card-body d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-building fa-2x"></i>
        </div>
        <h4 class="fw-bold text-primary mb-2">采用者目录</h4>
        <p class="text-muted small mb-3 flex-grow-1">
          探索正式采用 Kueue 管理多租户 Kubernetes 批处理集群的公司和机构。
        </p>
        <a href="../adopters/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          查看官方采用者 ➔
        </a>
      </div>
    </div>
  </div>

  <!-- Talks, Case Studies & Tutorials -->
  <div class="col-md-6">
    <div class="card h-100 feature-card bg-light border-0 rounded-3 p-3">
      <div class="card-body d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-file-alt fa-2x"></i>
        </div>
        <h4 class="fw-bold text-primary mb-2">演讲、案例研究与教程</h4>
        <p class="text-muted small mb-3 flex-grow-1">
          观看 KubeCon 演示、技术教程和工程博客，突出实际的 Kueue 部署故事。
        </p>
        <a href="../talks_and_presentations/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          探索演讲与文章 ➔
        </a>
      </div>
    </div>
  </div>
</div>

<!-- 2. AUTOMATED MEDIUM & BLOG FEED -->
<div class="mb-5 border-top pt-4">
  <div class="text-center mb-3">
    <h4 class="fw-bold text-primary mb-1">最新工程故事与博客</h4>
    <p class="text-muted small">来自 Kueue 社区的博客文章、深度剖析和技术教程。</p>
  </div>
  
  {{< medium-feed >}}
  
  <p class="text-end small mt-3 mb-0">
    <a href="https://medium.com/tag/kueue" target="_blank" rel="noopener" class="text-info text-decoration-none fw-semibold">
      在 Medium 上查看所有 Kueue 文章 <i class="fas fa-external-link-alt ms-1 small"></i>
    </a>
  </p>
</div>
