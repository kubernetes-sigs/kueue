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

from mark_step_done import LOG_MARKER_START, mark_step_done

REPO_ROOT = Path(__file__).resolve().parents[2]


class CommandLineTest(unittest.TestCase):
    def test_standalone_helper_marks_step_without_adding_a_log(self) -> None:
        template = (REPO_ROOT / ".github/ISSUE_TEMPLATE/NEW_RELEASE.md").read_text()
        log = f"{LOG_MARKER_START}\nexisting log\n<!-- release-log-end -->\n"
        cases = {
            "new release issue": (template, "prepare-pull-release"),
            "existing release log": (template + log, "prepare-pull-main"),
            "already completed step": (mark_step_done(template, "ci-pull"), "ci-pull"),
            "older template": ("- [ ] Run prepare_pull.sh\n", "prepare-pull-release"),
            "unknown step": (template, "unknown-step"),
        }
        with tempfile.TemporaryDirectory() as directory:
            helper = Path(directory) / "mark_step_done.py"
            helper.write_text((REPO_ROOT / "hack/releasing/mark_step_done.py").read_text())
            for name, (body, command) in cases.items():
                with self.subTest(name=name):
                    result = subprocess.run(
                        [sys.executable, "-I", str(helper), command],
                        input=body, capture_output=True, text=True, check=False, cwd=directory,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, mark_step_done(body, command))
                    self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
