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

"""Update the [[params.versions]] dropdown in site/hugo.toml for a new release.

Usage: update-docs-versions.py <hugo-toml> <minor-version> <release-branch>
  hugo-toml:      path to site/hugo.toml
  minor-version:  e.g. v0.20 (major.minor, with leading "v")
  release-branch: e.g. release-0.20, or "main" when updating the main branch

On a release branch (release-branch != "main"):
  - Sets archived_version = false -> true
  - Updates the top-level githubbranch to the release branch name
  - Prepends a new [[params.versions]] entry for this release

On main (release-branch == "main"):
  - Prepends a new [[params.versions]] entry for this release
  - Drops the oldest release entry when the total exceeds MAX_RELEASE_ENTRIES

Both paths are idempotent: the prepend is skipped if the version already exists.
The drop policy keeps N-2 supported releases plus one extra for documentation access.
"""
import re
import sys

# N-2 actively supported releases + 1 extra for documentation access
MAX_RELEASE_ENTRIES = 4


def main():
    if len(sys.argv) != 4:
        print(
            f"Usage: {sys.argv[0]} <hugo-toml> <minor-version> <release-branch>",
            file=sys.stderr,
        )
        sys.exit(1)

    hugo_toml_path, minor_version, release_branch = sys.argv[1], sys.argv[2], sys.argv[3]
    # Derive entry values from minor_version, not release_branch:
    # release_branch is "main" on the main run but the entry should point to release-0.X
    entry_branch = "release-" + minor_version[1:]  # "v0.20" -> "release-0.20"
    subdomain = minor_version.replace(".", "-")     # "v0.20" -> "v0-20"

    with open(hugo_toml_path) as f:
        content = f.read()

    if release_branch != "main":
        content = content.replace("  archived_version = false", "  archived_version = true")
        # Replace only the first occurrence: the top-level githubbranch param,
        # not the githubbranch fields inside [[params.versions]] entries.
        content = re.sub(
            r'^  githubbranch = "main"',
            f'  githubbranch = "{release_branch}"',
            content,
            count=1,
            flags=re.MULTILINE,
        )

    # Prepend new entry only if this version is not already in the dropdown.
    if f'version = "{minor_version}"' not in content:
        new_entry = (
            f'\n  [[params.versions]]\n'
            f'    version = "{minor_version}"\n'
            f'    githubbranch = "{entry_branch}"\n'
            f'    url = "https://{subdomain}.kueue.sigs.k8s.io"\n'
        )
        marker = "  # These entries appear in the drop-down menu at the top of the website.\n"
        content = content.replace(marker, marker + new_entry, 1)

        # Drop the oldest release entry when the cap is exceeded.
        # Matches complete [[params.versions]] blocks for versioned releases (not "main").
        entry_re = re.compile(
            r'\n  \[\[params\.versions\]\]\n'
            r'    version = "v\d[^"]*"\n'
            r'    githubbranch = "[^"]+"\n'
            r'    url = "[^"]+"\n'
        )
        entries = list(entry_re.finditer(content))
        if len(entries) > MAX_RELEASE_ENTRIES:
            last = entries[-1]
            content = content[:last.start()] + content[last.end():]

    with open(hugo_toml_path, "w") as f:
        f.write(content)


if __name__ == "__main__":
    main()
