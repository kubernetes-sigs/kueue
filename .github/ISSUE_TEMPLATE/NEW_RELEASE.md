---
name: New Release
about: Propose a new release
title: Release v0.x.0
assignees: mimowo, tenzen-y

---

## Release Checklist
<!--
Please do not remove items from the checklist
-->
- [ ] [OWNERS](https://github.com/kubernetes-sigs/kueue/blob/main/OWNERS) must LGTM the release proposal.
  At least two for minor or major releases. At least one for a patch release.
- [ ] Verify that the changelog in this issue and the CHANGELOG folder is up-to-date
  - [ ] Use `/sync-release-notes` to generate and publish the release notes
- [ ] For major or minor releases (`v$MAJ.$MIN.0`), use the `/create-release-branch` ChatOps command to create a new release branch.
- [ ] Update the release branch:
  - [ ] Run `/prepare-pull release` ChatOps command (or locally: `./hack/releasing/prepare_pull.sh --target release $VERSION`)
  - [ ] Wait for this PR to merge <!-- PREPARE_PULL_RELEASE --> <!-- example #211 -->
- [ ] Versioned docs are handled automatically during releases -- major, minor, and patch: the
      `main`-update PR from `prepare_pull.sh` runs `hack/releasing/snapshot-docs.py`, which
      freezes the release's docs into `site/content/<locale>/v$MAJ.$MIN/docs`, adds it to the
      version dropdown, and prunes old snapshots. Patch releases re-freeze the existing snapshot
      to the new patch version (e.g. v0.17.7 -> v0.17.8). No Netlify/DNS steps are required. Just
      confirm the snapshot dirs and the `[[params.versions]]` entry are present in that PR.
- [ ] Run ChatOps command `/tag-release` on this issue. This will:
  - Extract the changelog from the issue description.
  - Create the release tag at the tip of the release branch.
  - Push the tag upstream (triggers Prow to build and publish staging container image: `us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:$VERSION`).
- [ ] Run ChatOps command `/create-draft-release` on this issue. This will:
  - Extract the changelog from the issue description.
  - Create the draft release pointing out to the created tag.
  - Write the change log into the draft release.
  - Generate the artifacts in the `release-artifacts` folder.
  - Upload the files in the `release-artifacts` folder to the draft release.
- [ ] Promote images and Helm Charts to production:
  - [ ] Use `/wait-for-images` to await for the staging images.
  - [ ] Run `./hack/releasing/promote_pull.sh $VERSION` to submit the promotion PR
  - [ ] Wait for the PR to be merged <!-- K8S_IO_PULL --> <!-- example kubernetes/k8s.io#7899 -->
  - [ ] Use `/wait-for-prod-images` to verify that the promoted images are available.
- [ ] Publish the draft release prepared at the [GitHub releases page](https://github.com/kubernetes-sigs/kueue/releases).
      Link: <!-- example https://github.com/kubernetes-sigs/kueue/releases/tag/v0.1.0 -->
- [ ] Run the [openvex action](https://github.com/kubernetes-sigs/kueue/actions/workflows/openvex.yaml) to generate openvex data. The action will add the file to the release artifacts.
- [ ] Run the [SBOM action](https://github.com/kubernetes-sigs/kueue/actions/workflows/sbom.yaml) to generate the SBOM and add it to the release.
- [ ] Update the `main` branch :
  - [ ] Run `/prepare-pull main` ChatOps command (or locally: `./hack/releasing/prepare_pull.sh --target main $VERSION`)
        *Note: The script automatically detects if a newer version is already out and skips version updates if so. Specifying `--skip-version-updates` is not necessary in a default workflow.*
  - [ ] Wait for this PR to merge <!-- PREPARE_PULL_MAIN --> <!-- example #214 -->
  - [ ] Cherry-pick the pull request onto the `website` branch
- [ ] For major, minor, or patch releases, merge the `main` branch into the `website` branch to publish the updated documentation.
- [ ] Send an announcement email to `sig-scheduling@kubernetes.io` and `wg-batch@kubernetes.io` with the subject `[ANNOUNCE] kueue $VERSION is released`.   <!--Link: example https://groups.google.com/a/kubernetes.io/g/wg-batch/c/-gZOrSnwDV4 -->
- [ ] For a major or minor release, prepare the repo for the next version:
  - [ ] Create an unannotated _devel_ tag in the
        `main` branch, on the first commit that gets merged after the release
         branch has been created (presumably the README update commit above), and, push the tag:
        `DEVEL=v$MAJ.$(($MIN+1)).0-devel; git tag $DEVEL main && git push upstream $DEVEL`
        This ensures that the devel builds on the `main` branch will have a meaningful version number.
  - [ ] Create a milestone for the next minor release and update prow to set it automatically for new PRs:
        <!-- example https://github.com/kubernetes/test-infra/pull/30222 -->
  - [ ] Create the presubmits and the periodic jobs for the next patch release: <!-- CI_PULL -->
        <!-- example: https://github.com/kubernetes/test-infra/pull/34561 -->
  - [ ] Drop CI Jobs for testing the out-of-support branch: <!-- CI_PULL -->
        <!-- example: https://github.com/kubernetes/test-infra/pull/34562 -->


## Changelog

```markdown
Describe changes since the last release here.
```
