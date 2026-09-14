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

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

import authorize_release_actor


class AuthorizationTest(unittest.TestCase):
    def test_authorization(self) -> None:
        owners = "aliases:\n  release-team: [alice]\n  approvers: [bob]\n  empty: []\n"
        cases = [
            ("first alias", "alice", ["release-team", "approvers"], True),
            ("second alias", "bob", ["release-team", "approvers"], True),
            ("outside requested alias", "bob", ["release-team"], False),
            ("unknown actor", "eve", ["release-team", "approvers"], False),
            ("case sensitive", "Alice", ["release-team"], False),
            ("empty membership", "alice", ["empty"], False),
        ]
        for name, actor, aliases, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(
                    expected,
                    authorize_release_actor.is_authorized(actor, owners, aliases),
                )

    def test_invalid_configuration(self) -> None:
        cases = [
            ("empty document", "", ["release-team"], ValueError),
            ("sequence document", "[]", ["release-team"], ValueError),
            ("missing aliases", "{}", ["release-team"], ValueError),
            ("null aliases", "aliases: null", ["release-team"], ValueError),
            ("sequence aliases", "aliases: []", ["release-team"], ValueError),
            ("missing alias", "aliases: {}", ["release-team"], ValueError),
            ("scalar members", "aliases: {release-team: alice}", ["release-team"], ValueError),
            ("null members", "aliases: {release-team: null}", ["release-team"], ValueError),
            ("non-string member", "aliases: {release-team: [alice, 1]}", ["release-team"], ValueError),
            ("malformed YAML", "aliases: [", ["release-team"], yaml.YAMLError),
            # 命中前一个别名后仍需检查后续配置，避免配置错误被提前返回掩盖。
            ("later alias missing", "aliases: {release-team: [alice]}", ["release-team", "approvers"], ValueError),
            ("later alias invalid", "aliases: {release-team: [alice], approvers: bob}", ["release-team", "approvers"], ValueError),
        ]
        for name, owners, aliases, error in cases:
            with self.subTest(name=name):
                with self.assertRaises(error):
                    authorize_release_actor.is_authorized("alice", owners, aliases)


class CommandLineTest(unittest.TestCase):
    def test_exit_codes(self) -> None:
        script = Path(authorize_release_actor.__file__).resolve()
        cases = [
            ("authorized", b"aliases: {release-team: [alice]}", "alice", ["release-team"], 0),
            ("denied", b"aliases: {release-team: [alice]}", "eve", ["release-team"], 1),
            ("multiple aliases", b"aliases: {release-team: [], approvers: [alice]}", "alice", ["release-team", "approvers"], 0),
            ("malformed YAML", b"aliases: [", "alice", ["release-team"], 2),
            ("invalid aliases", b"aliases: []", "alice", ["release-team"], 2),
            ("later alias missing", b"aliases: {release-team: [alice]}", "alice", ["release-team", "approvers"], 2),
            ("missing file", None, "alice", ["release-team"], 2),
            ("invalid UTF-8", b"\xff", "alice", ["release-team"], 2),
        ]
        for name, content, actor, aliases, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                owners = Path(directory) / "OWNERS_ALIASES"
                if content is not None:
                    owners.write_bytes(content)
                command = [sys.executable, str(script), "--actor", actor, "--owners-file", str(owners)]
                for alias in aliases:
                    command.extend(["--alias", alias])
                result = subprocess.run(command, capture_output=True, text=True, check=False)
                self.assertEqual(expected, result.returncode, result.stderr)
                self.assertEqual("", result.stdout)
                if expected == 2:
                    self.assertTrue(result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                else:
                    self.assertEqual("", result.stderr)


if __name__ == "__main__":
    unittest.main()
