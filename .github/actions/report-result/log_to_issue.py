#!/usr/bin/env python3
import sys
import re
import os
from datetime import datetime

MARKER = "<!-- release-log-comment -->"
LOG_HEADER = "# Release log"
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

    comment_body = os.environ.get("COMMENT_BODY", "").strip()

    if alias:
        title = alias
    else:
        title = command.replace("-", " ").capitalize()
    
    new_entry = (
        f"<!-- entry-start title=\"{title}\" -->\n"
        f"## {title}"
    )
    if command:
        new_entry += f"\nCommand: /{command}"
    new_entry += f"\nTriggered by: @{actor}\nTimestamp: {timestamp}\nAction link: {action_link}\n\n{message}\n<!-- entry-end -->"

    if not comment_body or MARKER not in comment_body:
        if not cleanup:
            body = f"{MARKER}\n{LOG_HEADER}\n\n{new_entry}"
            print(body)
        else:
            print(f"{MARKER}\n{LOG_HEADER}")
        return

    # Split Details and History using the history section marker
    history_match = re.search(re.escape(HISTORY_SECTION_MARKER), comment_body)
    
    if history_match:
        details = comment_body[:history_match.start()].strip()
        history = comment_body[history_match.end():].strip()
        # Strip any leading ## History header to avoid duplicating it on reconstruction
        history = re.sub(r'^## History\s*', '', history, flags=re.IGNORECASE | re.MULTILINE).strip()
    else:
        details = comment_body.strip()
        history = ""

    # Find if an entry for command exists in the Details section
    entry_pattern = r'(<!-- entry-start title="' + re.escape(title) + r'" -->.*?<!-- entry-end -->)'
    entry_match = re.search(entry_pattern, details, re.DOTALL)

    old_entry = ""
    if entry_match:
        old_entry = entry_match.group(1).strip()
        # Remove old entry from log_part
        details = details[:entry_match.start()].rstrip() + "\n\n" + details[entry_match.end():].lstrip()
        details = details.strip()

    # Append new entry to Details section if not cleanup
    if not cleanup:
        if not re.search(r'^' + re.escape(LOG_HEADER) + r'\b', details, re.MULTILINE | re.IGNORECASE):
            details = f"{LOG_HEADER}\n\n{details}".strip()
        details = f"{details}\n\n{new_entry}".strip()

    # Handle History
    if old_entry:
        # Strip markers, header, and command from old_entry to avoid redundancy in history
        old_entry_clean = re.sub(r'<!-- entry-start title=".*?" -->\s*', '', old_entry)
        old_entry_clean = re.sub(r'\s*<!-- entry-end -->', '', old_entry_clean)
        old_entry_clean = re.sub(r'^## ' + re.escape(title) + r'\b\s*', '', old_entry_clean, flags=re.MULTILINE)
        old_entry_clean = re.sub(r'^Command:.*?\n\s*', '', old_entry_clean, flags=re.MULTILINE).strip()

        details_pattern = (
            r'(<!-- history-start title="' + re.escape(title) + r'" -->\s*<details>\s*<summary>\s*<b>' + re.escape(title) + r'\s+history</b>\s*</summary>\s*)'
            r'(.*?)'
            r'(\s*</details>\s*<!-- history-end -->)'
        )
        details_match = re.search(details_pattern, history, re.DOTALL | re.IGNORECASE)
        if details_match:
            prefix = details_match.group(1)
            content = details_match.group(2).strip()
            suffix = details_match.group(3)
            # Use horizontal rules to separate multiple archived runs of the same command
            new_content = f"{old_entry_clean}\n\n---\n\n{content}" if content else old_entry_clean
            history = history[:details_match.start()] + f"{prefix}\n{new_content}\n{suffix}" + history[details_match.end():]
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

    # Reconstruct final comment
    final_body = details
    if history:
        final_body += f"\n\n{HISTORY_SECTION_MARKER}\n\n## History\n\n{history}"

    if not final_body.startswith(MARKER):
        final_body = f"{MARKER}\n{final_body}"

    print(final_body)

if __name__ == "__main__":
    main()
