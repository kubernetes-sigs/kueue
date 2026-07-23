#!/usr/bin/env python3
import sys
import re
import os
from datetime import datetime

MARKER = "<!-- release-log-comment -->"
LOG_HEADER = "# Log"
HISTORY_HEADER = "# History"
DETAILS_START = "<details>\n<summary>History</summary>"
DETAILS_END = "</details>"

def main():
    command = os.environ.get("INPUT_COMMAND", "").strip()
    message = os.environ.get("INPUT_MESSAGE", "").strip()
    cleanup = os.environ.get("INPUT_CLEANUP", "").strip().lower() == "true"
    actor = os.environ.get("GITHUB_ACTOR", "").strip()
    run_id = os.environ.get("GITHUB_RUN_ID", "").strip()
    repository = os.environ.get("GITHUB_REPOSITORY", "").strip()
    server_url = os.environ.get("GITHUB_SERVER_URL", "https://github.com").strip()
    
    timestamp = datetime.utcnow().strftime("%Y-%m-%d %H:%M:%S UTC")
    action_link = f"{server_url}/{repository}/actions/runs/{run_id}"

    comment_body = os.environ.get("COMMENT_BODY", "").strip()

    title = command.replace("-", " ").capitalize()
    new_entry = f"## {title}\nCommand: /{command}\nTriggered by: {actor}\nTimestamp: {timestamp}\nAction link: {action_link}\n\n{message}"

    if not comment_body or MARKER not in comment_body:
        if not cleanup:
            body = f"{MARKER}\n{LOG_HEADER}\n\n{new_entry}"
            print(body)
        else:
            print(f"{MARKER}\n{LOG_HEADER}")
        return

    # Split Log and History
    history_match = re.search(r'^' + re.escape(HISTORY_HEADER) + r'\b', comment_body, re.MULTILINE | re.IGNORECASE)
    
    if history_match:
        log_part = comment_body[:history_match.start()].strip()
        history_part = comment_body[history_match.start():].strip()
    else:
        log_part = comment_body.strip()
        history_part = ""

    # Find if an entry for command exists in the Log section
    entry_pattern = r'(^## ' + re.escape(title) + r'\b.*?)(?=\n## |\n# |$)'
    entry_match = re.search(entry_pattern, log_part, re.DOTALL | re.MULTILINE)

    old_entry = ""
    if entry_match:
        old_entry = entry_match.group(1).strip()
        # Remove old entry from log_part
        log_part = log_part[:entry_match.start()].rstrip() + "\n\n" + log_part[entry_match.end():].lstrip()
        log_part = log_part.strip()

    # Append new entry to Log section if not cleanup
    if not cleanup:
        if not re.search(r'^' + re.escape(LOG_HEADER) + r'\b', log_part, re.MULTILINE | re.IGNORECASE):
            log_part = f"{LOG_HEADER}\n\n{log_part}".strip()
        log_part = f"{log_part}\n\n{new_entry}".strip()

    # Handle History
    if old_entry:
        if not history_part:
            history_part = f"{HISTORY_HEADER}\n\n{DETAILS_START}\n\n{old_entry}\n{DETAILS_END}"
        else:
            # Prepend old entry to details block
            details_pattern = r'(<details>\s*<summary>History</summary>\s*)(.*?)(\s*</details>)'
            details_match = re.search(details_pattern, history_part, re.DOTALL | re.IGNORECASE)
            if details_match:
                prefix = details_match.group(1)
                content = details_match.group(2).strip()
                suffix = details_match.group(3)
                new_content = f"{old_entry}\n\n{content}" if content else old_entry
                history_part = history_part[:details_match.start()] + f"{prefix}\n{new_content}\n{suffix}" + history_part[details_match.end():]
            else:
                history_part = history_part.rstrip() + f"\n\n{DETAILS_START}\n\n{old_entry}\n{DETAILS_END}"

    # Reconstruct final comment
    final_body = log_part
    if history_part:
        final_body += f"\n\n{history_part}"

    if not final_body.startswith(MARKER):
        final_body = f"{MARKER}\n{final_body}"

    print(final_body)

if __name__ == "__main__":
    main()
