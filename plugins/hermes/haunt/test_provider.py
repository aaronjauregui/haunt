"""Tests for the haunt memory provider.

Run with the python that has hermes-agent installed:

    python -m unittest discover plugins/hermes/haunt

Importing this module has no side effects: hermes's loader executes every
sibling ``*.py`` of a plugin, so haunt installs only ``__init__.py`` and
``plugin.yaml``, but a stray copy of this file must still be harmless.
"""

from __future__ import annotations

import importlib.util
import json
import os
import shutil
import socket
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
INSTALLED_FILES = ("__init__.py", "plugin.yaml")


def _load_plugin():
    name = "haunt_memory_plugin_under_test"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(
        name, HERE / "__init__.py", submodule_search_locations=[str(HERE)])
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


class FakeGhost:
    """A control socket that answers tool_call requests from a handler."""

    def __init__(self, path: Path, handler):
        self.path = path
        self.handler = handler
        self.requests = []
        self._sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self._sock.bind(str(path))
        self._sock.listen(8)
        self._thread = threading.Thread(target=self._serve, daemon=True)
        self._thread.start()

    def _serve(self):
        while True:
            try:
                conn, _ = self._sock.accept()
            except OSError:
                return
            with conn:
                buf = b""
                while b"\n" not in buf:
                    chunk = conn.recv(4096)
                    if not chunk:
                        break
                    buf += chunk
                req = json.loads(buf.split(b"\n", 1)[0])
                self.requests.append(req)
                reply = self.handler(req)
                if reply is not None:
                    conn.sendall((json.dumps(reply) + "\n").encode())

    def close(self):
        self._sock.close()
        try:
            self.path.unlink()
        except FileNotFoundError:
            pass


class ProviderTest(unittest.TestCase):
    def setUp(self):
        self.plugin = _load_plugin()
        self.tmp = tempfile.mkdtemp(prefix="haunt-plugin-test-")
        self.env = mock.patch.dict(os.environ, {
            "HAUNT_RUNTIME_DIR": self.tmp,
            "HAUNT_GHOST": "wisp",
        })
        self.env.start()
        self.ghost = None
        self.provider = self.plugin.HauntMemoryProvider()
        self.provider.initialize("s1", hermes_home=self.tmp, platform="cli")

    def tearDown(self):
        if self.ghost:
            self.ghost.close()
        self.env.stop()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def serve(self, handler):
        self.ghost = FakeGhost(Path(self.tmp) / "wisp.sock", handler)
        return self.ghost

    @staticmethod
    def result(text, is_error=False):
        return lambda req: {"type": "tool_result", "id": req["id"], "text": text, "is_error": is_error}

    # -- identity ------------------------------------------------------------

    def test_name_and_availability(self):
        self.assertEqual(self.provider.name, "haunt")
        # No socket yet: still available, the ghost may start later.
        self.assertTrue(self.provider.is_available())

    def test_ghost_name_from_profile_dir(self):
        with mock.patch.dict(os.environ, {"HAUNT_GHOST": ""}):
            self.assertEqual(self.plugin.resolve_ghost_name("/x/profiles/haunt-sam"), "sam")
            self.assertEqual(self.plugin.resolve_ghost_name("/x/profiles/pip"), "pip")
            self.assertEqual(self.plugin.resolve_ghost_name("/home/u/.hermes"), "")
            p = self.plugin.HauntMemoryProvider()
            p.initialize("s", hermes_home="/x/profiles/haunt-sam")
            self.assertTrue(p.is_available())

    def test_socket_path(self):
        sp = self.plugin.socket_path
        self.assertEqual(sp("wisp"), Path(self.tmp) / "wisp.sock")
        with mock.patch.dict(os.environ, {"HAUNT_RUNTIME_DIR": "", "XDG_RUNTIME_DIR": "/run/user/7"}):
            self.assertEqual(sp("wisp"), Path("/run/user/7/haunt/wisp.sock"))
        with mock.patch.dict(os.environ, {"HAUNT_RUNTIME_DIR": "", "XDG_RUNTIME_DIR": "", "TMPDIR": "/var/t"}):
            self.assertEqual(sp("wisp"), Path("/var/t/haunt/wisp.sock"))

    # -- tools ---------------------------------------------------------------

    def test_schemas(self):
        schemas = {s["name"]: s for s in self.provider.get_tool_schemas()}
        self.assertEqual(set(schemas), {"beliefs", "correct"})
        self.assertEqual(schemas["beliefs"]["parameters"]["required"], [])
        c = schemas["correct"]["parameters"]
        self.assertEqual(c["required"], ["subject"])
        self.assertEqual(c["properties"]["fact_id"]["type"], "integer")
        self.assertEqual(c["properties"]["truth"]["type"], "string")
        from agent.memory_manager import normalize_tool_schema
        for s in schemas.values():
            self.assertIs(normalize_tool_schema(s), s)

    def test_beliefs_round_trip(self):
        g = self.serve(self.result("Sam:\n  #3 works at Acme (you said)"))
        out = json.loads(self.provider.handle_tool_call("beliefs", {"subject": "Sam"}))
        self.assertEqual(out, {"result": "Sam:\n  #3 works at Acme (you said)"})
        self.assertEqual(g.requests[0]["type"], "tool_call")
        self.assertEqual(g.requests[0]["name"], "beliefs")
        self.assertEqual(g.requests[0]["args"], {"subject": "Sam"})
        self.assertTrue(g.requests[0]["id"])

    def test_correct_round_trip(self):
        g = self.serve(self.result("Sam no longer 'works at Acme'; now 'left in March'."))
        out = json.loads(self.provider.handle_tool_call(
            "correct", {"subject": "Sam", "fact_id": "3", "truth": "left in March"}))
        self.assertIn("left in March", out["result"])
        self.assertEqual(g.requests[0]["args"], {"subject": "Sam", "fact_id": 3, "truth": "left in March"})

    def test_correct_timeout_is_generous(self):
        seen = []
        real = socket.socket.settimeout

        def spy(sock, t):
            seen.append(t)
            return real(sock, t)

        self.serve(self.result("ok"))
        with mock.patch.object(socket.socket, "settimeout", spy):
            self.provider.handle_tool_call("correct", {"subject": "Sam"})
            self.provider.handle_tool_call("beliefs", {})
        self.assertEqual(seen, [600.0, 5.0])

    def test_correct_validation(self):
        self.serve(self.result("unused"))
        self.assertIn("error", json.loads(self.provider.handle_tool_call("correct", {})))
        self.assertIn("error", json.loads(self.provider.handle_tool_call(
            "correct", {"subject": "Sam", "fact_id": "three"})))
        self.assertIn("error", json.loads(self.provider.handle_tool_call("remember", {})))
        self.assertEqual(self.ghost.requests, [])

    def test_is_error_becomes_readable_error(self):
        self.serve(self.result("That was not allowed: the person said no.", is_error=True))
        out = json.loads(self.provider.handle_tool_call("correct", {"subject": "Sam"}))
        self.assertEqual(out["error"], "That was not allowed: the person said no.")

    def test_protocol_error_becomes_readable_error(self):
        self.serve(lambda req: {"type": "error", "id": req["id"], "error": "unknown tool"})
        out = json.loads(self.provider.handle_tool_call("beliefs", {}))
        self.assertIn("unknown tool", out["error"])

    def test_socket_down(self):
        out = json.loads(self.provider.handle_tool_call("beliefs", {"subject": "Sam"}))
        self.assertIn("wisp is not running", out["error"])
        self.assertEqual(self.provider.prefetch("how is Sam?"), "")

    def test_closed_without_answer(self):
        self.serve(lambda req: None)
        out = json.loads(self.provider.handle_tool_call("beliefs", {}))
        self.assertIn("without answering", out["error"])

    # -- recall --------------------------------------------------------------

    def test_prefetch_formats_beliefs(self):
        g = self.serve(self.result("Sam: #3 works at Acme\n"))
        got = self.provider.prefetch("how is Sam doing?", session_id="s1")
        self.assertEqual(got, self.plugin.PREFETCH_HEADER + "\nSam: #3 works at Acme")
        self.assertEqual(g.requests[0]["name"], "beliefs")
        self.assertEqual(g.requests[0]["args"], {"text": "how is Sam doing?"})

    def test_prefetch_empty(self):
        g = self.serve(self.result(""))
        self.assertEqual(self.provider.prefetch("nice weather"), "")
        self.assertEqual(self.provider.prefetch("   "), "")
        self.assertEqual(len(g.requests), 1)

    def test_prefetch_error_is_silent(self):
        self.serve(self.result("boom", is_error=True))
        self.assertEqual(self.provider.prefetch("Sam?"), "")

    def test_passive_hooks(self):
        self.assertIn("correct", self.provider.system_prompt_block())
        self.assertIsNone(self.provider.sync_turn("u", "a", session_id="s1"))
        self.assertIsNone(self.provider.on_memory_write("add", "user", "likes tea"))


class DiscoveryTest(unittest.TestCase):
    """The plugin loads through hermes's own discovery from $HERMES_HOME/plugins/haunt/."""

    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="haunt-hermes-home-")
        dest = Path(self.home) / "plugins" / "haunt"
        dest.mkdir(parents=True)
        for f in INSTALLED_FILES:
            shutil.copy(HERE / f, dest / f)
        self.env = mock.patch.dict(os.environ, {"HERMES_HOME": self.home, "HAUNT_GHOST": "wisp"})
        self.env.start()

    def tearDown(self):
        self.env.stop()
        shutil.rmtree(self.home, ignore_errors=True)

    def test_loads_by_name(self):
        from agent.memory_provider import MemoryProvider
        from plugins import memory as memory_plugins

        self.assertIn("haunt", memory_plugins.list_memory_provider_names())
        self.assertEqual(memory_plugins.find_provider_dir("haunt"), Path(self.home) / "plugins" / "haunt")
        provider = memory_plugins.load_memory_provider("haunt", register_skills=False)
        self.assertIsNotNone(provider)
        self.assertIsInstance(provider, MemoryProvider)
        self.assertEqual(provider.name, "haunt")
        self.assertTrue(provider.is_available())
        found = {n: avail for n, _, avail in memory_plugins.discover_memory_providers()}
        self.assertTrue(found.get("haunt"))

    def test_manager_accepts_tools(self):
        from agent.memory_manager import MemoryManager
        from plugins import memory as memory_plugins

        provider = memory_plugins.load_memory_provider("haunt", register_skills=False)
        mm = MemoryManager()
        mm.add_provider(provider)
        self.assertIs(mm._tool_to_provider.get("beliefs"), provider)
        self.assertIs(mm._tool_to_provider.get("correct"), provider)
        # Socket down: the manager still gets a JSON error, not an exception.
        with mock.patch.dict(os.environ, {"HAUNT_RUNTIME_DIR": self.home}):
            self.assertIn("error", json.loads(mm.handle_tool_call("beliefs", {})))


if __name__ == "__main__":
    unittest.main()
