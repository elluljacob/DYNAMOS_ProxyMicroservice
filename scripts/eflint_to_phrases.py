#!/usr/bin/env python3
"""
Convert an eFLINT file to the string format used by the "phrases" command.

The eFLINT server expects a line-based protocol: a single JSON object per line.
The command format is: {"command":"phrases","text":"<escaped eFLINT content>"}
Newlines and carriage returns in the JSON are replaced with spaces (to keep it on one line).

Usage:
    python eflint_to_phrases.py [path/to/file.eflint]
    python eflint_to_phrases.py  # uses configuration/eflint-models/VU.eflint (default)
"""

import argparse
import json
import sys
from pathlib import Path


def eflint_to_phrases_string(eflint_content: str) -> str:
    """Convert eFLINT content to the phrases command string (single line, ready to send)."""
    cmd = {"command": "phrases", "text": eflint_content}
    cmd_json = json.dumps(cmd, ensure_ascii=False)
    # Match Go implementation: replace newlines/carriage returns for line-based protocol
    cmd_str = cmd_json.replace("\n", " ").replace("\r", " ")
    return cmd_str


def main() -> None:
    default_path = Path(__file__).resolve().parent.parent / "configuration" / "eflint-models" / "VU.eflint"
    parser = argparse.ArgumentParser(description="Convert eFLINT file to phrases command string")
    parser.add_argument(
        "file",
        nargs="?",
        default=str(default_path),
        help=f"Path to eFLINT file (default: {default_path})",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="Show stats (text length, command size)",
    )
    args = parser.parse_args()

    path = Path(args.file)
    if not path.exists():
        print(f"Error: file not found: {path}", file=sys.stderr)
        sys.exit(1)

    text = path.read_text(encoding="utf-8")
    cmd_str = eflint_to_phrases_string(text)

    if args.verbose:
        print(f"# Text length: {len(text)} chars", file=sys.stderr)
        print(f"# Command size: {len(cmd_str)} chars", file=sys.stderr)
        print(file=sys.stderr)

    print(cmd_str)


if __name__ == "__main__":
    main()
