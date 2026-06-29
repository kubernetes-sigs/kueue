---
title: "Kueue 社区"
linkTitle: "社区"
weight: 30
menu:
  main:
    weight: 30
---


<!-- 1. 核心支柱卡片 (三栏布局) -->
<div class="row g-3 mb-5">
  
  <!-- 采用者展示卡片 -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-users fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">采用者展示</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          探索在生产运行环境中部署 Kueue 以管理大规模 AI/ML 和批处理工作负载的组织列表。
        </p>
        <a href="adopters/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          查看采用者 ➔
        </a>
      </div>
    </div>
  </div>

  <!-- 演讲与案例卡片 -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-video fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">演讲与案例研究</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          观看 KubeCon 演讲、主题演讲和技术演示，并阅读 Kueue 采用者精心撰写的工程案例研究。
        </p>
        <a href="talks_and_presentations/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          观看与阅读 ➔
        </a>
      </div>
    </div>
  </div>

  <!-- 贡献者专区卡片 -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-tools fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">贡献者专区</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          准备好帮助塑造批处理调度的未来了吗？阅读我们的指南，了解如何设置开发环境并提交您的第一个 PR。
        </p>
        <a href="contribution_guidelines/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          如何参与贡献 ➔
        </a>
      </div>
    </div>
  </div>

</div>

<!-- 2. 自动集成的 MEDIUM 博客动态 -->
<div class="mb-5 border-top pt-4">
  <div class="text-center mb-3">
    <h4 class="fw-bold text-primary mb-1">Medium 最新动态</h4>
    <p class="text-muted small">深度剖析、教程和社区文章。</p>
  </div>
  
  {{< medium-feed >}}
  
  <p class="text-end small mt-3 mb-0">
    <a href="https://medium.com/tag/kueue" target="_blank" rel="noopener" class="text-info text-decoration-none fw-semibold">
      在 Medium 上查看所有 Kueue 文章 <i class="fas fa-external-link-alt ms-1 small"></i>
    </a>
  </p>
</div>

<!-- 3. 社区联系页脚 -->
<div class="border-top pt-4">
  <div class="text-center mb-4">
    <h4 class="fw-bold text-primary mb-1">与社区互动</h4>
    <p class="text-muted small">获取帮助、参与讨论并获取最新动态。</p>
  </div>
  
  <div class="row g-3 justify-content-center">
    <div class="col-md-4">
      <div class="d-flex align-items-center bg-light border rounded-3 p-3">
        <div class="text-info me-3">
          <i class="fab fa-slack fa-lg"></i>
        </div>
        <div>
          <h6 class="fw-bold mb-1">Slack 频道</h6>
          <a href="https://kubernetes.slack.com/messages/wg-batch" target="_blank" rel="noopener" class="small text-info text-decoration-none fw-semibold">
            加入 #wg-batch ➔
          </a>
        </div>
      </div>
    </div>
    <div class="col-md-4">
      <div class="d-flex align-items-center bg-light border rounded-3 p-3">
        <div class="text-info me-3">
          <i class="fa fa-envelope fa-lg"></i>
        </div>
        <div>
          <h6 class="fw-bold mb-1">邮件列表</h6>
          <a href="https://groups.google.com/a/kubernetes.io/g/wg-batch" target="_blank" rel="noopener" class="small text-info text-decoration-none fw-semibold">
            订阅 wg-batch ➔
          </a>
        </div>
      </div>
    </div>
    <div class="col-md-4">
      <div class="d-flex align-items-center bg-light border rounded-3 p-3">
        <div class="text-info me-3">
          <i class="fas fa-calendar-check fa-lg"></i>
        </div>
        <div>
          <h6 class="fw-bold mb-1">社区例会</h6>
          <span class="small text-muted d-block">公开双周同步会议</span>
        </div>
      </div>
    </div>
  </div>
</div>
