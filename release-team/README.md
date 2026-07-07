# Kueue Release Steps

## Notes
- The proposal denotes the currently released version as `vX.Y.Z`.
- Step 3 is performed only for major releases: this means `Z=0`.

## Permission Hierarchy

`Kueue Owner` > `Release Team` > `Kueue Contributor`

`Upstream Contributor`

---

### Step 1: Release Proposal & Approvals
* **Role:** `Kueue Owner`
* **How to run:** Manual / Prow Gating (No script needed).
* **Process:** Open a release issue using the `NEW_RELEASE` template. Upstream owners must comment `/lgtm` and `/approve` on the issue.
* **Permissions:** Upstream Owner. This step cannot be automated because it requires human judgment to verify the release plan and milestones.

### Step 2: Generate and Publish Release Notes
* **Role:** `Release Team` or `Kueue Contributor`
* **How to run:** ChatOps (Recommended) or PR-Based Workflow.
* **Process:**
  * **Option A (ChatOps):**
    1. Comment `/sync-release-notes vX.Y.Z` on the release issue to generate the release notes and update the issue body.
    2. Comment `/create-release-pr vX.Y.Z` to create and push the release notes PR.
  * **Option B (Manual):**
    Execute the release notes sync script locally on your fork and submit the changes as a standard pull request:
    ```bash
    ./hack/releasing/sync-notes.sh $VERSION
    ```
* **Permissions:** Release Team Member (for ChatOps, requires matching username in `release-team/release-team.json`) or Standard Contributor (for Manual).

### Step 3: Create Release Branch (Only for Major releases)
* **Role:** `Release Team`
* **How to run:** ChatOps Command: `/create-branch vX.Y.0` on the release issue. ChatOps Script.
* **Permissions:** Release Team Member (Requires matching username in `release-team/release-team.json`). No local write access or GPG keys are needed.

### Step 4: Prepare Version-Bumping PR for Release Branch
* **Role:** `Kueue Contributor`
* **How to run:** PR-Based Workflow.
* **Process:** Generate version increments and open a Pull Request against the release branch:
  ```bash
  ./hack/releasing/prepare_pull.sh --target release $VERSION
  ```
* **Permissions:** Standard Contributor (Submitted via standard PR from your fork).

### Step 5: Tag Cutting & Draft Release (Core GHA Automation)
* **Role:** `Release Team`
* **How to run:** ChatOps Command: `/release vX.Y.Z` on the release issue. ChatOps Script.
* **Permissions:** Release Team Member (Requires matching username in `release-team/release-team.json`). Pushing tags and draft creation are securely executed by the `GITHUB_TOKEN`.

### Step 6: Promote Images to Production
* **Role:** `Kueue Contributor`
* **How to run:** Scripted PR.
* **Process:**
  1. Wait for Prow to build staging container images by running:
     ```bash
     ./hack/releasing/wait_for_images.sh $VERSION
     ```
  2. Generate and submit the promotion PR to `kubernetes/k8s.io`:
     ```bash
     ./hack/releasing/promote_pull.sh $VERSION
     ```
  3. Verify the promoted production images are available:
     ```bash
     ./hack/releasing/wait_for_images.sh --prod $VERSION
     ```
* **Permissions:** Standard Contributor (No registry credentials are held by the user; copy operations are managed securely by the CNCF Image Promoter).

### Step 7: Publish GitHub Release
* **Role:** `Release Team`
* **How to run:** ChatOps Command: `/publish-release vX.Y.Z`. ChatOps Script.
* **Permissions:** Release Team Member.

### Step 8: Generate SBOM & OpenVEX Data
* **Role:** `Kueue Contributor`
* **How to run:** Automated Webhooks (No manual scripts or triggers needed).
* **Process:** Once the draft release is published, Kueue's existing `.github/workflows/openvex.yaml` and `.github/workflows/sbom.yaml` listen to the `release: published` webhook and generate these files automatically.
* **Permissions:** Fully Automated (No user permissions required).

### Step 9: Update the main branch
* **Role:** `Kueue Contributor`
* **How to run:** PR-Based Workflow.
* **Process:** Prepare a version bump pull request against the main branch:
  ```bash
  ./hack/releasing/prepare_pull.sh --target main $VERSION
  ```
* **Permissions:** Standard Contributor (Submitted via standard PR from your fork).

### Step 10: Merge Documentation Updates to Website Branch
* **Role:** `Kueue Contributor`
* **How to run:** PR-Based Workflow.
* **Process:** Submit a standard PR merging `main` (containing your release updates) into the `website` branch to deploy the documentation.
* **Permissions:** Standard Contributor.

### Step 11: Send Release Announcement
* **Role:** `Upstream Contributor`
* **How to run:** MANUAL (Must be done manually; cannot be automated).
* **Process:** Send an announcement email to `sig-scheduling@kubernetes.io` and `wg-batch@kubernetes.io` with the subject `[ANNOUNCE] kueue $VERSION is released`.
* **Permissions:** Google Groups Membership (The member must be joined to these lists and have posting privileges).

### Step 12: Prepare Repo for the Next Version
* **Role:** `Upstream Contributor`, `Release Team`
* **How to run:** Hybrid (ChatOps & PR).
* **Process:**
  - **Create the unannotated devel tag:** Handled automatically as part of our `/release` ChatOps step.
  - **Create GitHub Milestones:** Handled automatically as part of our `/release` ChatOps step.
  - **Create presubmits and drop old out-of-support testing branches in Prow:** Must be done manually via PR. Because Prow jobs are managed inside the shared centralized repository `kubernetes/test-infra`, the operator must open a pull request against that repository.
* **Permissions:** Upstream Contributor (to submit PRs to `kubernetes/test-infra`).
