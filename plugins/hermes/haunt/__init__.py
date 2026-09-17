"""haunt — hermes memory provider backed by a running haunt ghost.

Haunt keeps a belief store about people (facts with provenance, where the
person's word outranks the ghost's). This provider is hermes's view of it:
per-turn recall of standing beliefs about people the turn names, and the
``beliefs`` / ``correct`` tools.

It talks only to the ghost's control socket (JSONL, one request per
connection), never to the ghost's database: the ghost is the sole writer of
its store. ``correct`` does not change anything by itself; the ghost asks the
person on screen and applies the change only on yes, so it can block for
minutes.

Haunt installs this directory as ``$HERMES_HOME/plugins/haunt/`` and selects
it with ``memory.provider: haunt``.
"""

from __future__ import annotations

import json
import logging
import os
import socket
import threading
from pathlib import Path
from typing import Any, Dict, List, Optional

from agent.memory_provider import MemoryProvider

logger = logging.getLogger(__name__)

BELIEFS_TIMEOUT_S = 5.0
CORRECT_TIMEOUT_S = 600.0
_MAX_REPLY_BYTES = 1 << 20

PREFETCH_HEADER = (
    "What you believe about people mentioned "
    "(from haunt; the person's word outranks yours):"
)

SYSTEM_NOTE = (
    "Beliefs about people come from haunt: read them with the `beliefs` tool "
    "and, when the person says one is wrong, fix it with the `correct` tool "
    "(the person confirms on screen)."
)

BELIEFS_SCHEMA = {
    "name": "beliefs",
    "description": (
        "List the standing beliefs haunt holds about a person or thing, each with "
        "its fact id and where it came from. Use it before quoting what you "
        "believe about someone, and to find the fact id to pass to `correct`. "
        "Without a subject, lists beliefs about everyone."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "subject": {
                "type": "string",
                "description": "Who or what to list beliefs about, e.g. 'Sam'.",
            },
        },
        "required": [],
    },
}

CORRECT_SCHEMA = {
    "name": "correct",
    "description": (
        "Propose a correction to a belief, from what the person just said "
        "(e.g. 'Sam left Acme in March'). Name the subject; give fact_id (from "
        "`beliefs`) to retire or replace a specific belief, and truth for what "
        "is true now. Nothing changes until the person confirms on screen, so "
        "this may take a while; the result says what was applied or that the "
        "person declined. The person's word outranks yours: never use this to "
        "overrule something the person told you."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "subject": {
                "type": "string",
                "description": "Who or what the belief is about.",
            },
            "fact_id": {
                "type": "integer",
                "description": "Id of the belief being corrected, as listed by `beliefs`.",
            },
            "truth": {
                "type": "string",
                "description": "What is true now, in the person's terms. Omit to only retire the belief.",
            },
        },
        "required": ["subject"],
    },
}

_TIMEOUTS = {"beliefs": BELIEFS_TIMEOUT_S, "correct": CORRECT_TIMEOUT_S}


def _hermes_home() -> Optional[Path]:
    env = os.environ.get("HERMES_HOME", "").strip()
    if env:
        return Path(env)
    try:
        from hermes_constants import get_hermes_home

        return get_hermes_home()
    except Exception:
        return None


def resolve_ghost_name(hermes_home: Optional[str] = None) -> str:
    """``$HAUNT_GHOST``, else the profile directory's name without a ``haunt-`` prefix."""
    name = os.environ.get("HAUNT_GHOST", "").strip()
    if name:
        return name
    home = Path(hermes_home) if hermes_home else _hermes_home()
    if home is None:
        return ""
    base = home.name
    if base.startswith("haunt-"):
        base = base[len("haunt-"):]
    # The bare default home (~/.hermes) names no ghost.
    if not base or base.startswith("."):
        return ""
    return base


def socket_path(ghost: str) -> Path:
    """Where haunt listens for ``ghost``; mirrors haunt's control.SocketPath."""
    runtime = os.environ.get("HAUNT_RUNTIME_DIR", "").strip()
    if runtime:
        return Path(runtime) / f"{ghost}.sock"
    xdg = os.environ.get("XDG_RUNTIME_DIR", "").strip()
    if xdg:
        return Path(xdg) / "haunt" / f"{ghost}.sock"
    # Go's os.TempDir: $TMPDIR, else /tmp.
    return Path(os.environ.get("TMPDIR") or "/tmp") / "haunt" / f"{ghost}.sock"


class HauntError(Exception):
    """A call that did not produce a tool result; the message is readable."""


class HauntMemoryProvider(MemoryProvider):
    def __init__(self) -> None:
        self._ghost = ""
        self._hermes_home: Optional[str] = None
        self._seq = 0
        self._lock = threading.Lock()

    @property
    def name(self) -> str:
        return "haunt"

    # -- lifecycle ---------------------------------------------------------

    def is_available(self) -> bool:
        # The ghost may start after hermes; only a name is required.
        return bool(resolve_ghost_name(self._hermes_home))

    def unavailable_reason(self) -> str:
        return "no haunt ghost name: set HAUNT_GHOST or run under a haunt-<ghost> profile"

    def initialize(self, session_id: str, **kwargs) -> None:
        home = kwargs.get("hermes_home")
        if home:
            self._hermes_home = str(home)
        self._ghost = resolve_ghost_name(self._hermes_home)

    def shutdown(self) -> None:
        pass

    # -- context -----------------------------------------------------------

    def system_prompt_block(self) -> str:
        return SYSTEM_NOTE

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        if not query or not query.strip():
            return ""
        try:
            text = self._call("beliefs", {"text": query})
        except HauntError as exc:
            logger.debug("haunt prefetch skipped: %s", exc)
            return ""
        except Exception as exc:  # never let recall break a turn
            logger.debug("haunt prefetch failed: %s", exc)
            return ""
        text = text.strip()
        if not text:
            return ""
        return f"{PREFETCH_HEADER}\n{text}"

    def sync_turn(self, user_content: str, assistant_content: str, *,
                  session_id: str = "", messages: Optional[List[Dict[str, Any]]] = None,
                  turn_author: Optional[Dict[str, Any]] = None) -> None:
        # The ghost already records what happened in a turn.
        return None

    def on_memory_write(self, action: str, target: str, content: str,
                        metadata: Optional[Dict[str, Any]] = None) -> None:
        # hermes's memory files stay hermes's.
        return None

    # -- tools -------------------------------------------------------------

    def get_tool_schemas(self) -> List[Dict[str, Any]]:
        return [BELIEFS_SCHEMA, CORRECT_SCHEMA]

    def handle_tool_call(self, tool_name: str, args: Dict[str, Any], **kwargs) -> str:
        if tool_name not in _TIMEOUTS:
            return _error(f"Unknown tool: {tool_name}")
        args = dict(args or {})
        if tool_name == "correct":
            if not str(args.get("subject") or "").strip():
                return _error("correct needs a subject")
            if args.get("fact_id") is not None:
                try:
                    args["fact_id"] = int(args["fact_id"])
                except (TypeError, ValueError):
                    return _error(f"fact_id must be an integer, not {args['fact_id']!r}")
        # Only pass what haunt's tools declare.
        allowed = {"beliefs": ("subject",), "correct": ("subject", "fact_id", "truth")}[tool_name]
        args = {k: v for k, v in args.items() if k in allowed and v is not None and v != ""}
        try:
            text = self._call(tool_name, args)
        except HauntError as exc:
            return _error(str(exc))
        except Exception as exc:
            return _error(f"That did not work: {exc}")
        return json.dumps({"result": text}, ensure_ascii=False)

    # -- socket ------------------------------------------------------------

    def _ghost_name(self) -> str:
        if not self._ghost:
            self._ghost = resolve_ghost_name(self._hermes_home)
        return self._ghost

    def _next_id(self) -> str:
        with self._lock:
            self._seq += 1
            return str(self._seq)

    def _call(self, tool: str, args: Dict[str, Any]) -> str:
        """One ``tool_call`` round trip; returns the result text or raises HauntError."""
        ghost = self._ghost_name()
        if not ghost:
            raise HauntError("haunt is not reachable: no ghost name (set HAUNT_GHOST)")
        path = socket_path(ghost)
        req_id = self._next_id()
        req = {"type": "tool_call", "id": req_id, "name": tool, "args": args}
        timeout = _TIMEOUTS.get(tool, BELIEFS_TIMEOUT_S)
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
                s.settimeout(timeout)
                s.connect(str(path))
                s.sendall((json.dumps(req) + "\n").encode("utf-8"))
                buf = b""
                while b"\n" not in buf:
                    chunk = s.recv(65536)
                    if not chunk:
                        break
                    buf += chunk
                    if len(buf) > _MAX_REPLY_BYTES:
                        raise HauntError(f"{ghost} sent an oversized reply")
        except (FileNotFoundError, ConnectionRefusedError):
            raise HauntError(f"{ghost} is not running (no haunt socket at {path})")
        except socket.timeout:
            raise HauntError(f"{ghost} did not answer {tool} within {timeout:g}s")
        except OSError as exc:
            raise HauntError(f"could not reach {ghost}: {exc}")
        line = buf.split(b"\n", 1)[0].strip()
        if not line:
            raise HauntError(f"{ghost} closed the connection without answering")
        try:
            reply = json.loads(line)
        except ValueError:
            raise HauntError(f"{ghost} sent an unreadable reply")
        kind = reply.get("type")
        if kind == "error":
            raise HauntError(f"That did not work: {reply.get('error') or 'unknown error'}")
        if kind != "tool_result":
            raise HauntError(f"{ghost} sent an unexpected reply type {kind!r}")
        text = str(reply.get("text") or "")
        if reply.get("is_error"):
            raise HauntError(text or "That did not work.")
        return text


def _error(message: str) -> str:
    try:
        from tools.registry import tool_error

        return tool_error(message)
    except Exception:
        return json.dumps({"error": message}, ensure_ascii=False)


def register(ctx) -> None:
    """Register the haunt memory provider with hermes."""
    ctx.register_memory_provider(HauntMemoryProvider())
