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

"""Add a release to the [[params.versions]] dropdown in site/hugo.toml.

Usage: update-docs-versions.py <hugo-toml> <minor-version>
  hugo-toml:     path to site/hugo.toml
  minor-version: e.g. v0.20 (major.minor, with leading "v")

Docs are versioned by path (Karpenter-style single deploy): "main" is the
development site at /docs/, and each release is a frozen snapshot served from
/v0.X/docs/. This script prepends a [[params.versions]] entry pointing at that
path and drops the oldest release once the total exceeds MAX_RELEASE_ENTRIES
(the two most recent releases: current and N-1; "main" tracks the upcoming
release and is kept separately). It is idempotent:
the prepend is skipped if the version already exists.

Called by hack/releasing/snapshot-docs.sh after the snapshot directory is created.
"""
import re
import sys

# Two most recent releases: current and N-1 ("main" is kept separately).
MAX_RELEASE_ENTRIES = 2


def main():
    if len(sys.argv) != 3:
        print(
            f"Usage: {sys.argv[0]} <hugo-toml> <minor-version>",
            file=sys.stderr,
        )
        sys.exit(1)

    hugo_toml_path, minor_version = sys.argv[1], sys.argv[2]

    with open(hugo_toml_path) as f:
        content = f.read()

    # Insert the new entry just after the "main" entry (kept first in the menu),
    # only if this version is not already present.
    if f'version = "{minor_version}"' not in content:
        new_entry = (
            f'\n  [[params.versions]]\n'
            f'    version = "{minor_version}"\n'
            f'    url = "/{minor_version}/docs/"\n'
        )
        main_entry = (
            '  [[params.versions]]\n'
            '    version = "main"\n'
            '    url = "/docs/"\n'
        )
        if main_entry not in content:
            print("error: could not find the 'main' [[params.versions]] entry", file=sys.stderr)
            sys.exit(1)
        content = content.replace(main_entry, main_entry + new_entry, 1)

        # Drop the oldest release entry when the cap is exceeded.
        # Matches complete [[params.versions]] blocks for versioned releases (not "main").
        entry_re = re.compile(
            r'\n  \[\[params\.versions\]\]\n'
            r'    version = "v\d[^"]*"\n'
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
