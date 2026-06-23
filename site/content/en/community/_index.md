---
title: "Kueue Community"
linkTitle: "Community"
weight: 30
menu:
  main:
    weight: 30
---


<!-- 1. CORE PILLARS (compelling content at first glance) -->
<div class="row g-3 mb-5">
  
  <!-- Adopters Card -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-users fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">Adopters Showcase</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          Explore the list of organizations running Kueue in production for large-scale AI/ML and batch workloads.
        </p>
        <a href="adopters/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          View Adopters ➔
        </a>
      </div>
    </div>
  </div>

  <!-- Talks Card -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-video fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">Talks & Case Studies</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          Watch KubeCon talks, keynotes, and technical demos, and read curated engineering case studies from Kueue adopters.
        </p>
        <a href="talks_and_presentations/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          Watch & Read ➔
        </a>
      </div>
    </div>
  </div>

  <!-- Contribute Card -->
  <div class="col-md-4">
    <div class="card h-100 feature-card bg-light border-0 rounded-3">
      <div class="card-body p-3 d-flex flex-column">
        <div class="text-info mb-2">
          <i class="fas fa-tools fa-lg"></i>
        </div>
        <h5 class="fw-bold text-primary mb-2">Contributor Zone</h5>
        <p class="text-muted small mb-3 flex-grow-1">
          Ready to help shape the future of batch scheduling? Read our guide on how to set up your dev environment and submit your first PR.
        </p>
        <a href="contribution_guidelines/" class="btn btn-sm btn-outline-info mt-auto align-self-start fw-semibold">
          How to Contribute ➔
        </a>
      </div>
    </div>
  </div>

</div>

<!-- 2. AUTOMATED MEDIUM FEED -->
<div class="mb-5 border-top pt-4">
  <div class="text-center mb-3">
    <h4 class="fw-bold text-primary mb-1">Latest from Medium</h4>
    <p class="text-muted small">Deep-dives, tutorials, and community articles.</p>
  </div>
  
  {{< medium-feed >}}
  
  <p class="text-end small mt-3 mb-0">
    <a href="https://medium.com/tag/kueue" target="_blank" rel="noopener" class="text-info text-decoration-none fw-semibold">
      View all Kueue articles on Medium <i class="fas fa-external-link-alt ms-1 small"></i>
    </a>
  </p>
</div>

<!-- 3. COMPACT CONNECT FOOTER -->
<div class="border-top pt-4">
  <div class="text-center mb-4">
    <h4 class="fw-bold text-primary mb-1">Connect with the Community</h4>
    <p class="text-muted small">Get help, join discussions, and stay updated.</p>
  </div>
  
  <div class="row g-3 justify-content-center">
    <div class="col-md-4">
      <div class="d-flex align-items-center bg-light border rounded-3 p-3">
        <div class="text-info me-3">
          <i class="fab fa-slack fa-lg"></i>
        </div>
        <div>
          <h6 class="fw-bold mb-1">Slack Channel</h6>
          <a href="https://kubernetes.slack.com/messages/wg-batch" target="_blank" rel="noopener" class="small text-info text-decoration-none fw-semibold">
            Join #wg-batch ➔
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
          <h6 class="fw-bold mb-1">Mailing List</h6>
          <a href="https://groups.google.com/a/kubernetes.io/g/wg-batch" target="_blank" rel="noopener" class="small text-info text-decoration-none fw-semibold">
            Subscribe to wg-batch ➔
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
          <h6 class="fw-bold mb-1">Community Meetings</h6>
          <span class="small text-muted d-block">Public bi-weekly syncs</span>
        </div>
      </div>
    </div>
  </div>
</div>
