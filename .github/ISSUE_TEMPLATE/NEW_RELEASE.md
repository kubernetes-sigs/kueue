---
name: New Release
about: Propose a new release
title: Release v0.x.0
assignees: ahg-g, alculquicondor, ArangoGutierrez, denkensk

---

## Release Checklist
<!--
Please do not remove items from the checklist
-->
- [ ] All [OWNERS](https://github.com/kubernetes-sigs/kueue/blob/main/OWNERS) must LGTM the release proposal
- [ ] Verify that the changelog in this issue is up-to-date
- [ ] For major or minor releases (v$MAJ.$MIN.0), create a new release branch.
  - [ ] an OWNER creates a vanilla release branch with
        `git branch release-$MAJ.$MIN main`
  - [ ] An OWNER pushes the new release branch with
        `git push release-$MAJ.$MIN`
- [ ] Update things like README, deployment templates, docs, configuration, test/e2e flags.
      Submit a PR against the release branch: <!-- example #211 #214 -->
- [ ] An OWNER [prepares a draft release](https://github.com/kubernetes-sigs/kueue/releases)
  - [ ] Write the change log into the draft release.
  - [ ] Run
      `make artifacts IMAGE_REGISTRY=k8s.gcr.io/kueue GIT_TAG=$VERSION`
      to generate the artifacts and upload the files in the `artifacts` folder
      to the draft release.
- [ ] An OWNER creates a signed tag running
     `git tag -s $VERSION`
      and inserts the changelog into the tag description.
      To perform this step, you need [a PGP key registered on github](https://docs.github.com/en/authentication/managing-commit-signature-verification/checking-for-existing-gpg-keys).
- [ ] An OWNER pushes the tag with
      `git push $VERSION`
  - Triggers prow to build and publish a staging container image
      `gcr.io/k8s-staging-kueue/kueue:$VERSION`
- [ ] Submit a PR against [k8s.io](https://github.com/kubernetes/k8s.io), 
      updating `k8s.gcr.io/images/k8s-staging-kueue/images.yaml` to
      [promote the container images](https://github.com/kubernetes/k8s.io/tree/main/k8s.gcr.io#image-promoter)
      to production: <!-- example kubernetes/k8s.io#3612-->
- [ ] Wait for the PR to be merged and verify that the image `k8s.gcr.io/kueue/kueue:$VERSION` is available.
- [ ] Publish the draft release prepared at the [Github releases page](https://github.com/kubernetes-sigs/kueue/releases).
- [ ] Add a link to the tagged release in this issue: <!-- example https://github.com/kubernetes-sigs/kueue/releases/tag/v0.1.0 -->
- [ ] Send an announcement email to `sig-scheduling@kubernetes.io` and `wg-batch@kubernetes.io` with the subject `[ANNOUNCE] kueue $VERSION is released`
- [ ] Add a link to the release announcement in this issue: <!-- example https://groups.google.com/a/kubernetes.io/g/wg-batch/c/-gZOrSnwDV4 -->
- [ ] For a major or minor release, update `README.md` and `docs/setup/install.md`
      in `main` branch: <!-- example #215 -->
- [ ] For a major or minor release, create an unannotated _devel_ tag in the
      `main` branch, on the first commit that gets merged after the release
       branch has been created (presumably the README update commit above), and, push the tag:
      `DEVEL=v0.$(($MAJ+1)).0-devel; git tag $DEVEL main && git push $DEVEL`
      This ensures that the devel builds on the `main` branch will have a meaningful version number.
- [ ] Close this issue


## Changelog
<!--
Describe changes since the last release here.
-->
