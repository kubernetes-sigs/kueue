#!/usr/bin/env python3
# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Freeze a release's docs into a versioned, path-based snapshot for the docs site.

Docs are versioned by path in a single Netlify deploy (Karpenter-style): the
development docs live at site/content/<locale>/docs (served at /docs/ and
/zh-cn/docs/), and each release is a frozen copy at
site/content/<locale>/v0.X/docs (served at /v0.X/docs/, /zh-cn/v0.X/docs/).
Every locale that has a docs/ tree is snapshotted; content is copied as-is and
not translated. No per-version subdomains, DNS, or Netlify config is involved.

After freezing and pruning the snapshot directories, it adds the release to the
[[params.versions]] dropdown in site/hugo.toml (dropping the oldest once the
retention window is exceeded).

Usage: snapshot-docs.py <minor-version> [<source-ref>]
  minor-version: e.g. v0.20 or 0.20
  source-ref:    git ref to snapshot docs from (default: release-<minor>).
                 The release branch is the source of truth for that release's
                 docs, not main (main is already ahead toward the next release).

Implemented in Python (rather than bash) so it runs unchanged across Linux and
macOS without depending on perl, GNU find/sort, or Bash 4 builtins.
"""
import io
import re
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

# Two most recent releases (current, N-1) are kept; "main" tracks the upcoming
# release and is served at the site root (kept separately in the dropdown).
MAX_VERSIONS = 2

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent.parent


def git(*args, ref=None):
    """Run a git command from the repo root and return its stdout as text."""
    return subprocess.run(
        ["git", *args],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout


def tree_has_entries(ref, path):
    """True if <path> exists as a non-empty tree at <ref>."""
    return bool(git("ls-tree", "--name-only", ref, "--", path).strip())


# Site params that name this release. The docs read these (via the `param`
# shortcode and templates like upgrade-policy-example) and would otherwise
# resolve to main's current, newer release, so an archived vX page would show
# the wrong version. They are pinned per-snapshot via the _index.md cascade
# (see rewrite_docs) rather than by rewriting the content, keeping snapshots
# free of per-release churn.
RELEASE_PARAMS = ("version", "chart_version")


def release_params(ref):
    """Read the release-naming params from the source ref's site/hugo.toml."""
    hugo_toml = git("show", f"{ref}:site/hugo.toml")
    values = {}
    for key in RELEASE_PARAMS:
        m = re.search(rf'^\s*{key}\s*=\s*"([^"]+)"', hugo_toml, re.MULTILINE)
        if m:
            values[key] = m.group(1)
    return values


def discover_locales(ref):
    """Locales under site/content that ship a docs/ tree in the source ref."""
    locales = []
    for line in git("ls-tree", "--name-only", ref, "site/content/").splitlines():
        loc = line.removeprefix("site/content/").strip("/")
        if loc and tree_has_entries(ref, f"site/content/{loc}/docs"):
            locales.append(loc)
    return locales


def extract_tree(ref, src, dst):
    """Snapshot <src>@<ref> into <dst> (replacing any existing <dst>)."""
    archive = subprocess.run(
        ["git", "archive", ref, "--", src],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
    ).stdout
    dst.parent.mkdir(parents=True, exist_ok=True)
    if dst.exists():
        shutil.rmtree(dst)
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        # git archive emits paths relative to the repo root (e.g. src/...);
        # extract into a temp area then move the subtree we asked for into place.
        staging = dst.parent / f".snapshot-staging-{dst.name}"
        if staging.exists():
            shutil.rmtree(staging)
        tar.extractall(staging)
        shutil.move(str(staging / src), str(dst))
        shutil.rmtree(staging)


def version_key(name):
    """Sort key mirroring `sort -V` for vMAJOR.MINOR[...] directory names."""
    return tuple(int(part) for part in re.findall(r"\d+", name))


def prune(parent, keep):
    """Remove vX snapshot dirs under <parent> beyond the <keep> most recent."""
    if not parent.is_dir():
        return
    versions = sorted(
        (d for d in parent.iterdir() if d.is_dir() and d.name.startswith("v0.")),
        key=lambda d: version_key(d.name),
    )
    for old in versions[:-keep] if keep else versions:
        print(f"  removing {old.relative_to(REPO_ROOT)}")
        shutil.rmtree(old)


def rewrite_docs(dst, short, params):
    """Apply the frozen-snapshot text rewrites in place across the snapshot tree."""
    # Rewrite internal doc links so the frozen snapshot navigates within itself.
    # Inserts /v0.X before /docs/ in markdown-link and absolute-URL contexts,
    # preserving an optional locale prefix (e.g. /zh-cn/docs/ -> /zh-cn/v0.X/docs/).
    # Non-docs links (/examples/, /images/, ...) are untouched.
    link_res = [
        (re.compile(r"(\]\()((?:/[a-z-]+)?)/docs/"), rf"\1\2/{short}/docs/"),
        (re.compile(r"(kueue\.sigs\.k8s\.io)((?:/[a-z-]+)?)/docs/"), rf"\1\2/{short}/docs/"),
    ]
    # Point {{< include "examples/..." >}} shortcodes at this version's frozen static
    # examples (snapshotted below), so old docs keep including their own example code
    # and don't break when main's static/examples diverges.
    include_pat = re.compile(r'(\{\{< *include +(?:file=)?")examples/')
    include_repl = rf"\1{short}/examples/"
    # Strip `menu:` frontmatter blocks: a snapshot re-declaring the dev page's menu
    # entries would add duplicate versioned items to the site nav.
    menu_re = re.compile(r"^menu:[ \t]*\n(?:[ \t]+.*\n)+", re.MULTILINE)

    for path in dst.rglob("*"):
        if path.suffix not in (".md", ".html") or not path.is_file():
            continue
        text = path.read_text()
        for pattern, repl in link_res:
            text = pattern.sub(repl, text)
        if path.suffix == ".md":
            text = include_pat.sub(include_repl, text)
            text = prefix_aliases(text, short)
            text = menu_re.sub("", text)
        path.write_text(text)

    # Inject snapshot-wide front matter into the section _index.md:
    #   - type: docs (+ cascade) so Docsy applies the docs layout (sidebar +
    #     content). Docsy keys that layout on the section/type being "docs", but
    #     nesting under /v0.X/docs makes Hugo's section "v0.X"; the cascade
    #     restores it for every page in the snapshot (else pages render blank).
    #   - the release-naming params (version/chart_version/docs_minor) as page
    #     params + a cascade, so {{< param "version" >}} and templates that read
    #     .Page.Param resolve to this release's values instead of main's current
    #     release. This freezes the version without rewriting page content.
    index = dst / "_index.md"
    if index.is_file():
        text = index.read_text()
        text = re.sub(r"\A---\n", _index_front_matter(short, params), text, count=1)
        index.write_text(text)


def _index_front_matter(short, params):
    """Build the injected _index.md front matter block (type + frozen params)."""
    def param_lines(indent):
        lines = [f"{indent}docs_minor: {short}"]
        lines += [f"{indent}{key}: {params[key]}" for key in RELEASE_PARAMS if key in params]
        return "\n".join(lines)

    return (
        "---\n"
        "type: docs\n"
        "params:\n"
        f"{param_lines('  ')}\n"
        "cascade:\n"
        "  type: docs\n"
        "  params:\n"
        f"{param_lines('    ')}\n"
    )


def prefix_aliases(text, short):
    """Version-prefix `aliases:` list targets (e.g. /docs/ -> /v0.X/docs/).

    Left as-is, each snapshot would redeclare the dev page's aliases verbatim and
    Hugo fatals on duplicate aliases; prefixing every alias with /v0.X keeps each
    version's redirects unique AND lets intra-version links that rely on an alias
    (e.g. the installation page's /docs/installation/) resolve inside the snapshot.
    """
    out = []
    in_aliases = False
    for line in text.splitlines(keepends=True):
        if re.match(r"^aliases:\s*$", line):
            in_aliases = True
            out.append(line)
            continue
        if in_aliases:
            if re.match(r"^\s*-\s", line):
                out.append(re.sub(r"^(\s*-\s*)/", rf"\1/{short}/", line, count=1))
                continue
            in_aliases = False
        out.append(line)
    return "".join(out)


def update_versions_menu(hugo_toml, short):
    """Add <short> to the [[params.versions]] dropdown in hugo.toml.

    Docs are versioned by path: "main" is the development site at /docs/, and each
    release is a frozen snapshot at /v0.X/docs/. Prepend a [[params.versions]]
    entry for this release just after the "main" entry and drop the oldest once the
    total exceeds MAX_VERSIONS. Idempotent: skipped if the version already exists.
    """
    content = hugo_toml.read_text()
    if f'version = "{short}"' in content:
        return

    main_entry = (
        '  [[params.versions]]\n'
        '    version = "main"\n'
        '    url = "/docs/"\n'
    )
    if main_entry not in content:
        print("error: could not find the 'main' [[params.versions]] entry", file=sys.stderr)
        sys.exit(1)
    new_entry = (
        f'\n  [[params.versions]]\n'
        f'    version = "{short}"\n'
        f'    url = "/{short}/docs/"\n'
    )
    content = content.replace(main_entry, main_entry + new_entry, 1)

    # Drop the oldest release entry when the cap is exceeded (matches complete
    # [[params.versions]] blocks for versioned releases, not "main").
    entry_re = re.compile(
        r'\n  \[\[params\.versions\]\]\n'
        r'    version = "v\d[^"]*"\n'
        r'    url = "[^"]+"\n'
    )
    entries = list(entry_re.finditer(content))
    if len(entries) > MAX_VERSIONS:
        last = entries[-1]
        content = content[:last.start()] + content[last.end():]

    hugo_toml.write_text(content)


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <minor-version> [<source-ref>]", file=sys.stderr)
        sys.exit(1)

    minor = sys.argv[1].removeprefix("v")   # 0.20
    short = f"v{minor}"                      # v0.20
    source_ref = sys.argv[2] if len(sys.argv) > 2 else f"release-{minor}"

    locales = discover_locales(source_ref)
    params = release_params(source_ref)

    for locale in locales:
        docs_src = f"site/content/{locale}/docs"
        dst = REPO_ROOT / "site" / "content" / locale / short / "docs"
        print(f"+++ [{locale}] snapshot {docs_src}@{source_ref} -> {dst.relative_to(REPO_ROOT)}")
        # Replace any prior snapshot for this version wholesale.
        version_root = REPO_ROOT / "site" / "content" / locale / short
        if version_root.exists():
            shutil.rmtree(version_root)
        extract_tree(source_ref, docs_src, dst)
        rewrite_docs(dst, short, params)

    # Freeze this version's static examples so the include shortcodes above resolve
    # against a versioned copy under /static/v0.X/examples instead of main's static.
    ex_src = "site/static/examples"
    if tree_has_entries(source_ref, ex_src):
        ex_dst = REPO_ROOT / "site" / "static" / short / "examples"
        print(f"+++ snapshot {ex_src}@{source_ref} -> {ex_dst.relative_to(REPO_ROOT)}")
        static_root = REPO_ROOT / "site" / "static" / short
        if static_root.exists():
            shutil.rmtree(static_root)
        extract_tree(source_ref, ex_src, ex_dst)

    # Prune snapshots beyond the retention window (oldest first), per locale.
    print(f"+++ Pruning old snapshots (keeping {MAX_VERSIONS})")
    for locale in locales:
        prune(REPO_ROOT / "site" / "content" / locale, MAX_VERSIONS)
    # Prune the matching versioned static examples too.
    prune(REPO_ROOT / "site" / "static", MAX_VERSIONS)

    # Update the shared version dropdown in hugo.toml (prepend + drop oldest).
    update_versions_menu(REPO_ROOT / "site" / "hugo.toml", short)

    print(f"+++ Done: snapshotted {short} for locales: {' '.join(locales)}")


if __name__ == "__main__":
    main()
