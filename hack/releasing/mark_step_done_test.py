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

import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from mark_step_done import LOG_MARKER_END, LOG_MARKER_START, mark_step_done

REPO_ROOT = Path(__file__).resolve().parents[2]
TEMPLATE_PATH = REPO_ROOT / ".github" / "ISSUE_TEMPLATE" / "NEW_RELEASE.md"
WORKFLOW_PATH = REPO_ROOT / ".github" / "workflows" / "release-utils.yml"


def template_markers() -> list[str]:
    # The same marker can be on several lines (e.g. a parent step shares the marker
    # of its last ChatOps sub-step), so return each command only once.
    return list(dict.fromkeys(re.findall(r"<!-- step:([a-z-]+) -->", TEMPLATE_PATH.read_text())))


def checked_lines(body: str) -> list[str]:
    return [line for line in body.splitlines() if "- [x]" in line]


class MarkStepDoneTest(unittest.TestCase):
    def test_template_markers_match_workflow_or_manual_commands(self) -> None:
        commands = set(re.findall(r'COMMAND="([a-z-]+)"', WORKFLOW_PATH.read_text()))
        for script in (REPO_ROOT / "hack/releasing").glob("*.sh"):
            commands.update(re.findall(r'(?:mark_step_done\.py|\$\{MARK_STEP_DONE_PY\})" ([a-z-]+)',
                                       script.read_text()))
        markers = template_markers()
        self.assertTrue(markers)
        # A marker that matches no command (typo, renamed or removed command)
        # would silently never be checked.
        self.assertLessEqual(set(markers), commands)

    def test_checks_only_the_items_of_the_command(self) -> None:
        template = TEMPLATE_PATH.read_text()
        for command in template_markers():
            with self.subTest(command):
                marker = f"<!-- step:{command} -->"
                want = [line.replace("- [ ]", "- [x]", 1) for line in template.splitlines() if marker in line]
                got = checked_lines(mark_step_done(template, command))
                # Exactly the lines tagged with this command's marker are checked, nothing else.
                self.assertEqual(got, want)

    def test_wait_for_prod_images_also_checks_promote_step(self) -> None:
        # "/wait-for-prod-images" is the last sub-step of "Promote images and Helm
        # Charts to production", so its success also completes the parent step.
        lines = checked_lines(mark_step_done(TEMPLATE_PATH.read_text(), "wait-for-prod-images"))
        self.assertEqual(len(lines), 2)
        self.assertIn("Promote images and Helm Charts to production:", lines[0])
        self.assertIn("`/wait-for-prod-images`", lines[1])

    def test_release_log_is_not_changed(self) -> None:
        template = TEMPLATE_PATH.read_text()
        log = (
            f"{LOG_MARKER_START}\n"
            "- [ ] copied text <!-- step:tag-release -->\n"
            f"{LOG_MARKER_END}"
        )
        marked = mark_step_done(f"{template}\n{log}", "tag-release")
        self.assertTrue(marked.endswith(log))
        self.assertEqual(len(checked_lines(marked)), 1)

    def test_issue_from_older_template_is_not_changed(self) -> None:
        old_template = re.sub(r" <!-- step:[a-z-]+ -->", "", TEMPLATE_PATH.read_text())
        self.assertEqual(mark_step_done(old_template, "tag-release"), old_template)


class CommandLineTest(unittest.TestCase):
    def test_standalone_helper_marks_step_without_adding_a_log(self) -> None:
        template = TEMPLATE_PATH.read_text()
        log = f"{LOG_MARKER_START}\nexisting log\n{LOG_MARKER_END}\n"
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
