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

"""Mark a release checklist step in the issue body read from stdin."""

import argparse
import re
import sys

LOG_MARKER_START = "<!-- release-log-start -->"
LOG_MARKER_END = "<!-- release-log-end -->"
STEP_MARKER = "<!-- step:{} -->"


def mark_step_done(issue_body: str, command: str) -> str:
    """Check the release checklist item tagged with <!-- step:<command> -->.
    Only unchecked items ("- [ ]") on the same line as the marker are changed,
    and only in the part of the body before the release log, so log messages
    can never affect the checklist. Bodies without the marker (e.g. issues
    created from an older template) are returned unchanged.
    """
    log_start = issue_body.find(LOG_MARKER_START)
    if log_start == -1:
        checklist, rest = issue_body, ""
    else:
        checklist, rest = issue_body[:log_start], issue_body[log_start:]

    marker = re.escape(STEP_MARKER.format(command))
    pattern = re.compile(r'^([ \t]*[-*][ \t]+)\[ \]([^\n]*' + marker + r')', re.MULTILINE)
    return pattern.sub(r'\1[x]\2', checklist) + rest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("step", help="Checklist step to mark as done.")
    args = parser.parse_args()
    sys.stdout.write(mark_step_done(sys.stdin.read(), args.step))
