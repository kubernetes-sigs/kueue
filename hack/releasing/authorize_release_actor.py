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
from pathlib import Path
from typing import Any

import yaml


def is_authorized(
    actor: str, owners_aliases: str, authorized_aliases: list[str]
) -> bool:
    """Return whether the actor belongs to an alias allowed to run release operations."""
    document: Any = yaml.safe_load(owners_aliases)
    if not isinstance(document, dict) or not isinstance(document.get("aliases"), dict):
        raise ValueError("OWNERS_ALIASES must contain an aliases mapping.")

    aliases = document["aliases"]
    authorized = False
    for alias in authorized_aliases:
        if alias not in aliases:
            raise ValueError(f"OWNERS_ALIASES must define alias {alias!r}.")
        members = aliases[alias]
        if not isinstance(members, list) or not all(
            isinstance(member, str) for member in members
        ):
            raise ValueError(
                f"OWNERS_ALIASES alias {alias!r} must contain a list of names."
            )
        if actor in members:
            authorized = True
    return authorized


def main() -> int:
    """Read OWNERS_ALIASES and return the authorization result."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--actor", required=True)
    parser.add_argument("--owners-file", required=True, type=Path)
    parser.add_argument("--alias", action="append", required=True)
    args = parser.parse_args()

    try:
        owners_aliases = args.owners_file.read_text(encoding="utf-8")
        authorized = is_authorized(args.actor, owners_aliases, args.alias)
    except (OSError, ValueError, yaml.YAMLError) as error:
        parser.exit(2, f"{error}\n")
    return 0 if authorized else 1


if __name__ == "__main__":
    raise SystemExit(main())
