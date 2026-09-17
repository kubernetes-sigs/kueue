---
title: "用户展示"
linkTitle: "用户展示"
weight: 31
menu:
  main:
    weight: 31
---

<!-- USER SHOWCASE HEADER -->
<div class="text-center mb-5">
  <h2 class="fw-bold text-primary mb-2">用户展示与案例研究</h2>
  <p class="lead text-muted mx-auto" style="max-width: 750px;">
    探索各大组织如何在生产环境中运行 Kueue，以管理大规模 AI/ML、批处理和高性能工作负载。
  </p>
</div>

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

  <!-- Talks & Case Studies -->
  <div class="col-md-6">
    <div class="card h-100 feature-card bg-light border-0 rounded-3 p-3">
      <div class="card-body d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-file-alt fa-2x"></i>
        </div>
        <h4 class="fw-bold text-primary mb-2">案例研究与主题演讲</h4>
        <p class="text-muted small mb-3 flex-grow-1">
          观看 KubeCon 演示、技术演示和工程博客，突出实际的 Kueue 部署故事。
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
    <h4 class="fw-bold text-primary mb-1">最新工程案例研究与博客</h4>
    <p class="text-muted small">来自 Kueue 采用者的博客文章、深度剖析和技术教程。</p>
  </div>
  
  {{< medium-feed >}}
  
  <p class="text-end small mt-3 mb-0">
    <a href="https://medium.com/tag/kueue" target="_blank" rel="noopener" class="text-info text-decoration-none fw-semibold">
      在 Medium 上查看所有案例研究 <i class="fas fa-external-link-alt ms-1 small"></i>
    </a>
  </p>
</div>
