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

import contextlib
import io
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

import authorize_release_actor


class IsAuthorizedTest(unittest.TestCase):
    def test_authorization_results(self) -> None:
        owners_aliases = (
            "aliases:\n"
            "  release-team:\n"
            "    - alice\n"
            "  kueue-approvers:\n"
            "    - bob\n"
        )

        test_cases = {
            "actor in first alias": ("alice", True),
            "actor in second alias": ("bob", True),
            "actor in no alias": ("carol", False),
        }
        for name, (actor, want) in test_cases.items():
            with self.subTest(name=name):
                self.assertEqual(
                    want,
                    authorize_release_actor.is_authorized(
                        actor,
                        owners_aliases,
                        ["release-team", "kueue-approvers"],
                    ),
                )

    def test_rejects_invalid_alias_documents(self) -> None:
        test_cases = {
            "non-mapping document": ("[]", "must contain an aliases mapping"),
            "non-mapping aliases": (
                "aliases: []\n",
                "must contain an aliases mapping",
            ),
            "missing requested alias": (
                "aliases:\n  release-team:\n    - alice\n",
                "must define alias 'kueue-approvers'",
            ),
            "non-list members": (
                "aliases:\n  release-team: alice\n",
                "must contain a list of names",
            ),
            "non-string member": (
                "aliases:\n  release-team:\n    - 1\n",
                "must contain a list of names",
            ),
            "invalid later alias after authorization": (
                "aliases:\n"
                "  release-team:\n"
                "    - alice\n"
                "  kueue-approvers: bob\n",
                "must contain a list of names",
            ),
        }

        for name, (owners_aliases, want_error) in test_cases.items():
            with self.subTest(name=name):
                with self.assertRaisesRegex(ValueError, want_error):
                    authorize_release_actor.is_authorized(
                        "alice",
                        owners_aliases,
                        ["release-team", "kueue-approvers"],
                    )

    def test_rejects_malformed_yaml(self) -> None:
        with self.assertRaisesRegex(yaml.YAMLError, "while parsing a flow node"):
            authorize_release_actor.is_authorized(
                "alice",
                "aliases: [\n",
                ["release-team"],
            )


class MainTest(unittest.TestCase):
    def test_exit_codes(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            owners_file = Path(temp_dir) / "OWNERS_ALIASES"
            owners_file.write_text(
                "aliases:\n  release-team:\n    - alice\n",
                encoding="utf-8",
            )

            for actor, want in (("alice", 0), ("bob", 1)):
                with self.subTest(actor=actor):
                    with mock.patch.object(
                        sys,
                        "argv",
                        [
                            "authorize_release_actor.py",
                            "--actor",
                            actor,
                            "--owners-file",
                            str(owners_file),
                            "--alias",
                            "release-team",
                        ],
                    ):
                        self.assertEqual(want, authorize_release_actor.main())

    def test_configuration_error_exits_with_two(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            owners_file = Path(temp_dir) / "OWNERS_ALIASES"
            owners_file.write_text("[]", encoding="utf-8")
            stderr = io.StringIO()

            with mock.patch.object(
                sys,
                "argv",
                [
                    "authorize_release_actor.py",
                    "--actor",
                    "alice",
                    "--owners-file",
                    str(owners_file),
                    "--alias",
                    "release-team",
                ],
            ):
                with contextlib.redirect_stderr(stderr):
                    with self.assertRaises(SystemExit) as got:
                        authorize_release_actor.main()

            self.assertEqual(2, got.exception.code)
            self.assertIn("must contain an aliases mapping", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
