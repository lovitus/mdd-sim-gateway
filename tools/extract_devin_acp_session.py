#!/usr/bin/env python3
"""Interactively export a local Devin Desktop ACP session to Markdown.

The ACP cache stores visible agent messages locally but may omit user-message bodies. This
tool intentionally exports only agent-visible replies and explicitly records that limitation.
It never exports agent thoughts, tool-call payloads, or local secrets.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import sqlite3
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


DEFAULT_DATA_ROOT = Path.home() / "Library/Application Support/Devin"
DEFAULT_OUTPUT_ROOT = Path("/Volumes/micron512g/tmp-project/codex-audit-tmp")
SESSION_INFO_PREFIX = "windsurf.acp.sessioninfo.session."
MESSAGE_INDEX_KEY = "windsurf.acp.messageStore.index"
PROMPT_LOG_RE = re.compile(
    r"^(?P<time>\S+).*acp_bridge_dispatch\{method=\"session/prompt\".*"
    r"await_turn_completion.*session shelled-selenium")
SAVED_NODES_RE = re.compile(
    r"^(?P<time>\S+).*Saved (?P<count>\d+) message nodes "
    r"\(starting from (?P<start>\d+)\) for session (?P<session>[\w-]+)")
SECRET_PATTERNS = (
    re.compile(r"(?im)^(\s*(?:password|token|api[_ -]?key|secret)\s*[:=]\s*).+$"),
    re.compile(r"(?i)(authorization:\s*bearer\s+)[^\s`]+"),
)


@dataclass(frozen=True)
class SessionCandidate:
    db_path: Path
    session_key: str
    title: str
    cwd: str
    first_position: int


def sqlite_connection(path: Path) -> sqlite3.Connection:
    return sqlite3.connect(f"file:{path}?mode=ro", uri=True)


def text_from_agent_payload(payload: str) -> str:
    """Extract only visible agent text from an ACP agent_message payload."""
    try:
        document = json.loads(payload)
    except (TypeError, json.JSONDecodeError):
        return ""
    fragments: list[str] = []
    for item in document.get("content", []):
        content = item.get("content") if isinstance(item, dict) else None
        if isinstance(content, dict) and isinstance(content.get("text"), str):
            fragments.append(content["text"])
    return "".join(fragments)


def redact(text: str) -> str:
    for pattern in SECRET_PATTERNS:
        text = pattern.sub(r"\1[REDACTED]", text)
    return text


def load_session_metadata(data_root: Path) -> tuple[dict[str, dict], dict[str, str]]:
    database = data_root / "User/globalStorage/state.vscdb"
    if not database.exists():
        return {}, {}
    sessions: dict[str, dict] = {}
    message_index: dict[str, str] = {}
    with sqlite_connection(database) as connection:
        rows = connection.execute("SELECT key, value FROM ItemTable").fetchall()
    for key, value in rows:
        if key == MESSAGE_INDEX_KEY:
            try:
                message_index = {
                    item: str(meta.get("uuid") or "")
                    for item, meta in json.loads(value).items()
                }
            except (TypeError, json.JSONDecodeError):
                pass
        elif str(key).startswith(SESSION_INFO_PREFIX):
            try:
                sessions[str(key)[len(SESSION_INFO_PREFIX):]] = json.loads(value).get("info") or {}
            except (TypeError, json.JSONDecodeError):
                pass
    return sessions, message_index


def agent_message_rows(path: Path) -> Iterable[tuple[int, str]]:
    with sqlite_connection(path) as connection:
        try:
            rows = connection.execute(
                "SELECT position, payload FROM messages WHERE kind='agent_message' ORDER BY position"
            ).fetchall()
        except sqlite3.DatabaseError:
            return []
    return [(int(position), text_from_agent_payload(payload)) for position, payload in rows]


def find_candidates(data_root: Path, phrase: str) -> list[SessionCandidate]:
    sessions, message_index = load_session_metadata(data_root)
    wanted = phrase.casefold()
    candidates: list[SessionCandidate] = []
    for database in sorted((data_root / "User/acp-messages").glob("*.db")):
        for position, text in agent_message_rows(database):
            if wanted not in text.casefold():
                continue
            session_key = next((key for key, uuid in message_index.items()
                                if uuid == database.stem), database.stem)
            metadata = sessions.get(session_key) or {}
            candidates.append(SessionCandidate(
                database, session_key, str(metadata.get("title") or "(untitled)"),
                str(metadata.get("cwd") or ""), position))
            break
    return candidates


def prompt_timestamps(data_root: Path, session_name: str) -> list[str]:
    timestamps: list[str] = []
    for log_path in (data_root / "logs").glob("**/*.log"):
        try:
            for line in log_path.read_text(errors="replace").splitlines():
                if session_name not in line or "method=\"session/prompt\"" not in line:
                    continue
                if "await_turn_completion" in line:
                    timestamps.append(line.split(" ", 1)[0])
        except OSError:
            continue
    return sorted(set(timestamps))


def persisted_times(data_root: Path, session_name: str) -> dict[int, str]:
    events: list[tuple[str, int, int]] = []
    for log_path in (data_root / "logs").glob("**/*.log"):
        try:
            for line in log_path.read_text(errors="replace").splitlines():
                match = SAVED_NODES_RE.match(line)
                if match and match["session"] == session_name:
                    events.append((match["time"], int(match["start"]), int(match["count"])))
        except OSError:
            continue
    result: dict[int, str] = {}
    for timestamp, start, count in sorted(events):
        for position in range(start, start + count):
            result.setdefault(position, timestamp)
    return result


def export_markdown(candidate: SessionCandidate, data_root: Path, output_root: Path) -> Path:
    session_name = candidate.session_key.rsplit("/", 1)[-1]
    session_meta, _index = load_session_metadata(data_root)
    metadata = session_meta.get(candidate.session_key) or {}
    output_root.mkdir(parents=True, exist_ok=True)
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output_path = output_root / f"devin-session-{session_name}-{stamp}.md"
    node_times = persisted_times(data_root, session_name)
    prompts = prompt_timestamps(data_root, session_name)
    lines = [
        "# Devin local ACP session extract", "",
        f"- Session: `{candidate.session_key}`",
        f"- Title: `{metadata.get('title') or candidate.title}`",
        f"- Workspace: `{metadata.get('cwd') or candidate.cwd}`",
        f"- Extracted: `{stamp}`",
        "- Source: local Devin Desktop ACP cache.",
        "- Scope: visible Agent messages only; internal thoughts, tool payloads and secrets excluded.",
        "- Limitation: local ACP storage may retain user-message count and prompt lifecycle timestamps, but not user-message bodies.",
        "",
        "## User-message timeline", "",
        "The local ACP store did not retain the user-message bodies. The following are prompt lifecycle timestamps, not guaranteed send timestamps.",
        "",
        "| # | UTC lifecycle timestamp | User body |",
        "|---:|---|---|",
    ]
    for number, timestamp in enumerate(prompts, 1):
        lines.append(f"| {number} | {timestamp} | unavailable in local ACP storage |")
    if not prompts:
        lines.append("| — | unavailable | unavailable in local ACP storage |")
    for position, text in agent_message_rows(candidate.db_path):
        if not text:
            continue
        timestamp = node_times.get(position, "unavailable")
        lines.extend(["", f"## Agent message {position} (persisted ~ {timestamp})", "", redact(text)])
    output_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return output_path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--query", help="A distinctive fragment from a visible Agent reply")
    parser.add_argument("--data-root", type=Path, default=DEFAULT_DATA_ROOT)
    parser.add_argument("--output-dir", type=Path, default=DEFAULT_OUTPUT_ROOT)
    args = parser.parse_args()
    phrase = args.query or input("Paste a distinctive fragment from the Devin Agent reply: ").strip()
    if not phrase:
        print("No search text supplied.", file=sys.stderr)
        return 2
    candidates = find_candidates(args.data_root, phrase)
    if not candidates:
        print("No local ACP session matched that Agent reply.", file=sys.stderr)
        return 1
    print("\nMatching sessions:")
    for number, item in enumerate(candidates, 1):
        print(f"  {number}. {item.title} | {item.session_key} | {item.cwd} | message {item.first_position}")
    selected = input("Export which session number? [q to cancel] ").strip()
    if selected.casefold() == "q":
        return 0
    try:
        candidate = candidates[int(selected) - 1]
    except (IndexError, ValueError):
        print("Invalid selection.", file=sys.stderr)
        return 2
    confirm = input(f"Export '{candidate.title}' to Markdown? [y/N] ").strip().casefold()
    if confirm not in {"y", "yes"}:
        print("Cancelled.")
        return 0
    path = export_markdown(candidate, args.data_root, args.output_dir)
    print(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
