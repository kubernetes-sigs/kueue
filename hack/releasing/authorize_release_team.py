#!/usr/bin/env python3

# Copyright 2026 The Kubernetes Authors.
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

import argparse
import re
from pathlib import Path


AUTHORIZED_ALIASES = {"kueue-approvers", "release-team"}
ALIAS_RE = re.compile(r"^  ([a-zA-Z0-9_-]+):\s*$")
MEMBER_RE = re.compile(r"^    -\s+([a-zA-Z0-9-]+)(?:\s+#.*)?$")


def is_authorized(actor: str, owners_aliases: str) -> bool:
    """Return whether the actor belongs to an alias allowed to run release operations."""
    current_alias = None
    for line in owners_aliases.splitlines():
        if alias_match := ALIAS_RE.fullmatch(line):
            current_alias = alias_match.group(1)
            continue

        if current_alias in AUTHORIZED_ALIASES:
            member_match = MEMBER_RE.fullmatch(line)
            if member_match and member_match.group(1) == actor:
                return True

    return False


def main() -> int:
    """Read OWNERS_ALIASES and return the authorization result."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--actor", required=True)
    parser.add_argument("--owners-file", required=True, type=Path)
    args = parser.parse_args()

    try:
        owners_aliases = args.owners_file.read_text(encoding="utf-8")
    except OSError as error:
        parser.exit(2, f"{error}\n")
    return 0 if is_authorized(args.actor, owners_aliases) else 1


if __name__ == "__main__":
    raise SystemExit(main())
