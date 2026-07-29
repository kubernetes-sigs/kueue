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

import sys
import re
import os
from datetime import datetime

LOG_MARKER_START = "<!-- release-log-start -->"
LOG_MARKER_END = "<!-- release-log-end -->"
LOG_HEADER = "## Release log"
HISTORY_SECTION_MARKER = "<!-- history-section-start -->"

def main():
    command = os.environ.get("INPUT_COMMAND", "").strip()
    alias = os.environ.get("INPUT_ALIAS", "").strip()
    if not command and not alias:
        print("Error: Neither command nor alias was provided.", file=sys.stderr)
        sys.exit(1)
    message = os.environ.get("INPUT_MESSAGE", "").strip()
    cleanup = os.environ.get("INPUT_CLEANUP", "").strip().lower() == "true"

    actor = os.environ.get("GITHUB_ACTOR", "").strip()
    run_id = os.environ.get("GITHUB_RUN_ID", "").strip()
    repository = os.environ.get("GITHUB_REPOSITORY", "").strip()
    server_url = os.environ.get("GITHUB_SERVER_URL", "https://github.com").strip()
    
    timestamp = datetime.utcnow().strftime("%Y-%m-%d %H:%M:%S UTC")
    action_link = f"{server_url}/{repository}/actions/runs/{run_id}"

    issue_body = os.environ.get("ISSUE_BODY", "").strip()

    if alias:
        title = alias
    else:
        title = command.replace("-", " ").capitalize()
    
    new_entry = (
        f"<!-- entry-start title=\"{title}\" -->\n"
        f"### {title}"
    )
    if command:
        new_entry += f"\nCommand: /{command}"
    new_entry += f"\nTriggered by: @{actor}\nTimestamp: {timestamp}\nAction link: {action_link}\n\n{message}\n<!-- entry-end -->"

    if LOG_MARKER_START not in issue_body:
        if not cleanup:
            log_part = f"{LOG_HEADER}\n\n{new_entry}"
        else:
            log_part = f"{LOG_HEADER}"
        updated_body = f"{issue_body}\n\n{LOG_MARKER_START}\n{log_part}\n{LOG_MARKER_END}"
        print(updated_body)
        return

    start_idx = issue_body.find(LOG_MARKER_START)
    end_idx = issue_body.find(LOG_MARKER_END)
    
    if start_idx == -1 or end_idx == -1:
        print("Error: release-log-start or release-log-end marker is missing or corrupted in the issue body.", file=sys.stderr)
        sys.exit(1)

    prefix = issue_body[:start_idx + len(LOG_MARKER_START)].strip()
    suffix = issue_body[end_idx:].strip()
    log_part = issue_body[start_idx + len(LOG_MARKER_START):end_idx].strip()

    # Split Details and History using the history section marker
    history_match = re.search(re.escape(HISTORY_SECTION_MARKER), log_part)
    
    if history_match:
        details = log_part[:history_match.start()].strip()
        history = log_part[history_match.end():].strip()
        # Strip any leading ## History header to avoid duplicating it on reconstruction
        history = re.sub(r'^###? History\s*', '', history, flags=re.IGNORECASE | re.MULTILINE).strip()
    else:
        details = log_part.strip()
        history = ""

    # Find all existing entries in the Details section
    entries = re.findall(r'(<!-- entry-start title=".*?" -->.*?<!-- entry-end -->)', details, re.DOTALL)

    entry_index = -1
    for idx, entry in enumerate(entries):
        if f'title="{title}"' in entry:
            entry_index = idx
            break

    old_entry = ""
    if entry_index != -1:
        old_entry = entries[entry_index].strip()
        if cleanup:
            entries.pop(entry_index)
        else:
            entries[entry_index] = new_entry
    elif not cleanup:
        entries.append(new_entry)

    # Reconstruct details section with horizontal rule separators
    if entries:
        details = f"{LOG_HEADER}\n\n" + "\n\n---\n\n".join(entries)
    else:
        details = LOG_HEADER

    # Handle History
    if old_entry:
        # Strip markers, header, and command from old_entry to avoid redundancy in history
        old_entry_clean = re.sub(r'<!-- entry-start title=".*?" -->\s*', '', old_entry)
        old_entry_clean = re.sub(r'\s*<!-- entry-end -->', '', old_entry_clean)
        old_entry_clean = re.sub(r'^###? ' + re.escape(title) + r'\b\s*', '', old_entry_clean, flags=re.MULTILINE)
        old_entry_clean = re.sub(r'^Command:.*?\n\s*', '', old_entry_clean, flags=re.MULTILINE).strip()

        details_pattern = (
            r'(<!-- history-start title="' + re.escape(title) + r'" -->\s*<details>\s*<summary>\s*<b>' + re.escape(title) + r'\s+history</b>\s*</summary>\s*)'
            r'(.*?)'
            r'(\s*</details>\s*<!-- history-end -->)'
        )
        details_match = re.search(details_pattern, history, re.DOTALL | re.IGNORECASE)
        if details_match:
            prefix_hist = details_match.group(1)
            content_hist = details_match.group(2).strip()
            suffix_hist = details_match.group(3)
            # Use horizontal rules to separate multiple archived runs of the same command
            new_content_hist = f"{old_entry_clean}\n\n---\n\n{content_hist}" if content_hist else old_entry_clean
            history = history[:details_match.start()] + f"{prefix_hist}\n{new_content_hist}\n{suffix_hist}" + history[details_match.end():]
        else:
            new_details_block = (
                f"<!-- history-start title=\"{title}\" -->\n"
                f"<details>\n"
                f"<summary><b>{title} history</b></summary>\n\n"
                f"{old_entry_clean}\n"
                f"</details>\n"
                f"<!-- history-end -->"
            )
            if history:
                history = history.rstrip() + f"\n\n{new_details_block}"
            else:
                history = new_details_block

    # Reconstruct final log part
    updated_log_part = details
    if history:
        updated_log_part += f"\n\n{HISTORY_SECTION_MARKER}\n\n### History\n\n{history}"

    if not updated_log_part.startswith(LOG_HEADER):
        updated_log_part = f"{LOG_HEADER}\n\n{updated_log_part}"

    # Reconstruct full issue body
    final_body = f"{prefix}\n{updated_log_part}\n{suffix}"
    print(final_body)

if __name__ == "__main__":
    main()
