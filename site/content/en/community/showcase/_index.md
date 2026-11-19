---
title: "Adopters, Talks & Resources"
linkTitle: "Adopters & Resources"
description: "Explore organizations running Kueue in production, conference talks, technical tutorials, and the latest engineering stories from the community."
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
        <h4 class="fw-bold text-primary mb-2">Adopters Directory</h4>
        <p class="text-muted small mb-3 flex-grow-1">
          Explore the list of organizations running Kueue in production for large-scale AI/ML and batch workloads.
        </p>
        <a href="../adopters/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          View Official Adopters ➔
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
        <h4 class="fw-bold text-primary mb-2">Talks, Case Studies & Tutorials</h4>
        <p class="text-muted small mb-3 flex-grow-1">
          Watch KubeCon presentations, technical tutorials, and engineering blogs highlighting real-world Kueue deployment stories.
        </p>
        <a href="../talks_and_presentations/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          Explore Presentations & Articles ➔
        </a>
      </div>
    </div>
  </div>
</div>

<!-- 2. AUTOMATED MEDIUM & BLOG FEED -->
<div class="mb-5 border-top pt-4">
  <div class="text-center mb-3">
    <h4 class="fw-bold text-primary mb-1">Latest Engineering Stories & Blogs</h4>
    <p class="text-muted small">Blog posts, deep-dives, and technical tutorials from the Kueue community.</p>
  </div>
  
  {{< medium-feed >}}
  
  <p class="text-end small mt-3 mb-0">
    <a href="https://medium.com/tag/kueue" target="_blank" rel="noopener" class="text-info text-decoration-none fw-semibold">
      View all Kueue stories on Medium <i class="fas fa-external-link-alt ms-1 small"></i>
    </a>
  </p>
</div>
