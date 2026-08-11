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

import unittest

import yaml

from authorize_release_team import is_authorized


OWNERS_ALIASES = """\
aliases:
  kueue-approvers:
    - approver
  release-team:
    - releaser
"""


class IsAuthorizedTest(unittest.TestCase):
    def test_authorized_aliases(self) -> None:
        cases = {
            "release-team member is authorized": (
                "releaser",
                ["release-team"],
                True,
            ),
            "approver is not implicitly authorized": (
                "approver",
                ["release-team"],
                False,
            ),
            "explicit additional alias is authorized": (
                "approver",
                ["release-team", "kueue-approvers"],
                True,
            ),
            "unknown actor is not authorized": (
                "unknown",
                ["release-team", "kueue-approvers"],
                False,
            ),
        }

        for name, (actor, aliases, want) in cases.items():
            with self.subTest(name):
                self.assertEqual(want, is_authorized(actor, OWNERS_ALIASES, aliases))

    def test_invalid_owners_aliases(self) -> None:
        cases = {
            "invalid yaml": ("aliases: [", yaml.YAMLError),
            "missing aliases mapping": ("config: {}", ValueError),
            "alias members are not a list": (
                "aliases:\n  release-team: releaser\n",
                ValueError,
            ),
            "alias member is not a string": (
                "aliases:\n  release-team:\n    - nested: releaser\n",
                ValueError,
            ),
        }

        for name, (owners_aliases, want_error) in cases.items():
            with self.subTest(name):
                with self.assertRaises(want_error):
                    is_authorized("releaser", owners_aliases, ["release-team"])


if __name__ == "__main__":
    unittest.main()
