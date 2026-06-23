#!/usr/bin/env bash
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

# Freeze a release's docs into a versioned, path-based snapshot for the docs site.
#
# Docs are versioned by path in a single Netlify deploy (Karpenter-style): the
# development docs live at site/content/<locale>/docs (served at /docs/ and
# /zh-cn/docs/), and each release is a frozen copy at
# site/content/<locale>/v0.X/docs (served at /v0.X/docs/, /zh-cn/v0.X/docs/).
# Every locale that has a docs/ tree is snapshotted; content is copied as-is and
# not translated. No per-version subdomains, DNS, or Netlify config is involved.
#
# Usage: snapshot-docs.sh <minor-version> [<source-ref>]
#   minor-version: e.g. v0.20 or 0.20
#   source-ref:    git ref to snapshot docs from (default: release-<minor>).
#                  The release branch is the source of truth for that release's
#                  docs, not main (main is already ahead toward the next release).
set -o errexit
set -o nounset
set -o pipefail

# Two most recent releases (current, N-1); keep in sync with update-docs-versions.py.
# "main" tracks the upcoming release and is served at the site root.
readonly MAX_VERSIONS=2

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." &>/dev/null && pwd)"
cd "${REPO_ROOT}"

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <minor-version> [<source-ref>]" >&2
  exit 1
fi

minor="${1#v}"                     # 0.20
short="v${minor}"                  # v0.20
source_ref="${2:-release-${minor}}"

# Locales that ship a docs/ tree in the source ref (e.g. en, zh-CN).
mapfile -t locales < <(
  git ls-tree --name-only "${source_ref}" site/content/ \
    | sed 's#site/content/##; s#/##' \
    | while read -r loc; do
        git ls-tree --name-only "${source_ref}" -- "site/content/${loc}/docs" | grep -q . && echo "${loc}"
      done
)

for locale in "${locales[@]}"; do
  docs_src="site/content/${locale}/docs"
  dst="site/content/${locale}/${short}/docs"

  echo "+++ [${locale}] snapshot ${docs_src}@${source_ref} -> ${dst}"
  tmp="$(mktemp -d)"
  git archive "${source_ref}" -- "${docs_src}" | tar -x -C "${tmp}"
  rm -rf "site/content/${locale}/${short}"
  mkdir -p "$(dirname "${dst}")"
  mv "${tmp}/${docs_src}" "${dst}"
  rm -rf "${tmp}"

  # Rewrite internal doc links so the frozen snapshot navigates within itself.
  # Inserts /v0.X before /docs/ in markdown-link and absolute-URL contexts,
  # preserving an optional locale prefix (e.g. /zh-cn/docs/ -> /zh-cn/v0.X/docs/).
  # Non-docs links (/examples/, /images/, ...) are untouched.
  rewrite="s{(\\]\\()((?:/[a-z-]+)?)/docs/}{\$1\$2/${short}/docs/}g; "
  rewrite+="s{(kueue\\.sigs\\.k8s\\.io)((?:/[a-z-]+)?)/docs/}{\$1\$2/${short}/docs/}g"
  find "${dst}" -type f \( -name '*.md' -o -name '*.html' \) -exec perl -i -pe "${rewrite}" {} +

  # Point {{< include "examples/..." >}} shortcodes at this version's frozen static
  # examples (snapshotted below), so old docs keep including their own example code
  # and don't break when main's static/examples diverges.
  incfix="s#(\\{\\{< *include +(?:file=)?\")examples/#\$1${short}/examples/#g"
  find "${dst}" -type f -name '*.md' -exec perl -i -pe "${incfix}" {} +

  # Version-prefix `aliases:` targets and strip `menu:` frontmatter. Left as-is,
  # each snapshot would redeclare the dev page's aliases verbatim and Hugo fatals
  # on duplicate aliases; prefixing every alias with /v0.X keeps each version's
  # redirects unique AND lets intra-version links that rely on an alias (e.g. the
  # installation page's /docs/installation/) resolve inside the snapshot. Menu
  # entries are dropped since they'd add duplicate versioned items to the site nav.
  # shellcheck disable=SC2016  # perl program: $ina/$1 are perl vars, not shell
  alias_prefix='
    if (/^aliases:\s*$/) { $ina = 1; print; next; }
    if ($ina) {
      if (/^\s*-\s/) { s{^(\s*-\s*)/}{$1/SNAPSHOT_VERSION/}; print; next; }
      $ina = 0;
    }
    print;
  '
  find "${dst}" -type f -name '*.md' -exec perl -i -ne "${alias_prefix//SNAPSHOT_VERSION/${short}}" {} +
  find "${dst}" -type f -name '*.md' -exec perl -0777 -i -pe 's/^menu:[ \t]*\n(?:[ \t]+.*\n)+//mg;' {} +

  # Force `type: docs` on the snapshot so Docsy applies the docs layout (sidebar +
  # content). Docsy keys that layout on the section/type being "docs", but nesting
  # under /v0.X/docs makes Hugo's section "v0.X"; the cascade restores it for all
  # pages in the snapshot. Without this the versioned pages render blank.
  if [[ -f "${dst}/_index.md" ]]; then
    perl -0777 -i -pe 's/\A---\n/---\ntype: docs\ncascade:\n  type: docs\n/' "${dst}/_index.md"
  fi
done

# Freeze this version's static examples so the include shortcodes above resolve
# against a versioned copy under /static/v0.X/examples instead of main's static.
ex_src="site/static/examples"
ex_dst="site/static/${short}/examples"
if git ls-tree --name-only "${source_ref}" -- "${ex_src}" | grep -q .; then
  echo "+++ snapshot ${ex_src}@${source_ref} -> ${ex_dst}"
  tmp="$(mktemp -d)"
  git archive "${source_ref}" -- "${ex_src}" | tar -x -C "${tmp}"
  rm -rf "site/static/${short}"
  mkdir -p "$(dirname "${ex_dst}")"
  mv "${tmp}/${ex_src}" "${ex_dst}"
  rm -rf "${tmp}"
fi

# Prune snapshots beyond the retention window (oldest first), per locale.
echo "+++ Pruning old snapshots (keeping ${MAX_VERSIONS})"
for locale in "${locales[@]}"; do
  mapfile -t versions < <(find "site/content/${locale}" -maxdepth 1 -type d -name 'v0.*' -printf '%f\n' 2>/dev/null | sort -V)
  if (( ${#versions[@]} > MAX_VERSIONS )); then
    drop=$(( ${#versions[@]} - MAX_VERSIONS ))
    for old in "${versions[@]:0:${drop}}"; do
      echo "  removing site/content/${locale}/${old}"
      rm -rf "site/content/${locale}/${old}"
    done
  fi
done
# Prune the matching versioned static examples too.
mapfile -t static_versions < <(find site/static -maxdepth 1 -type d -name 'v0.*' -printf '%f\n' 2>/dev/null | sort -V)
if (( ${#static_versions[@]} > MAX_VERSIONS )); then
  drop=$(( ${#static_versions[@]} - MAX_VERSIONS ))
  for old in "${static_versions[@]:0:${drop}}"; do
    echo "  removing site/static/${old}"
    rm -rf "site/static/${old}"
  done
fi

# Update the shared version dropdown in hugo.toml (prepend + drop oldest).
python3 "${SCRIPT_DIR}/update-docs-versions.py" site/hugo.toml "${short}"

echo "+++ Done: snapshotted ${short} for locales: ${locales[*]}"
