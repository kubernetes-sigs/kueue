# Release Strategy

## Versioning

kindkit follows [semantic versioning](https://semver.org/):

* **v0.x.y** (current) — the API is not guaranteed to be stable. Breaking changes bump the minor version (for example, `v0.2.0`). Bug fixes and backward-compatible changes bump the patch version (for example, `v0.1.1`).
* **v1.0.0** — signals API stability. After v1, breaking changes require a new major version and module path (for example, `github.com/IrvingMg/kindkit/v2`).

kindkit will remain in v0 until the API has been validated through real usage and is stable enough to support long term.

## Support policy

kindkit supports only the latest released version. Backports to older release lines are not planned at this stage.

## Creating a release

Go modules are published through git tags. There is no separate build artifact or package registry to publish to.

Create an annotated tag and push it:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Then create a GitHub Release with release notes:

```bash
gh release create v0.1.0 --title "v0.1.0" --notes "$(cat <<'EOF'
Initial public release.

- Change one
- Change two
EOF
)"
```

For subsequent releases, `--generate-notes` can generate release notes automatically from commits and pull requests since the previous tag:

```bash
gh release create v0.1.1 --title "v0.1.1" --generate-notes
```

## Patch releases

For the currently supported release line:

1. Fix the bug on `main` and commit the change.
2. Tag and push the new version:

   ```bash
   git tag -a v0.1.1 -m "v0.1.1"
   git push origin v0.1.1
   ```
3. Create a GitHub Release.

## Backporting (future, if needed)

This section only applies if kindkit later needs to support multiple release lines at the same time.

For example, if both `v0.1.x` and `v0.2.x` need fixes, create or update a release branch and cherry-pick the change:

```bash
git checkout -b release-0.1 v0.1.0
# cherry-pick or apply the fix
git tag -a v0.1.1 -m "v0.1.1"
git push origin release-0.1 v0.1.1
```

This is not needed while only the latest version is supported.
