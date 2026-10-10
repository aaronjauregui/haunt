"""Every toolset a hermes image knows, as JSON on stdout, for
TestHermesToolsets (hermescheck_test.go).

Runs inside the image on its own venv, with a throwaway HERMES_HOME and no
network. It imports hermes's own tables rather than reading its source:
the static TOOLSETS (toolsets.py), the tool registry the tools/*.py
modules and the plugins register into (tools/registry.py), and the
plugins (hermes_cli/plugins.py). Every plugin found is enabled in the
throwaway config first, so a bundled plugin the person has not turned on
still shows the toolset it would bring. Nothing starts a gateway, calls a
model or installs a package.
"""

import json
import logging
import os
import sys
import tempfile

home = tempfile.mkdtemp(prefix="hermes-toolsets-")
os.environ["HERMES_HOME"] = home
os.environ.pop("HERMES_SAFE_MODE", None)
logging.disable(logging.WARNING)

# Hermes prints to stdout as it loads; only the JSON goes there.
out = sys.stdout
sys.stdout = sys.stderr

from hermes_cli.plugins_discovery import collect_directory_manifests, manifest_key  # noqa: E402

manifests = collect_directory_manifests()
# JSON is YAML, and the image's venv has no YAML writer.
with open(os.path.join(home, "config.yaml"), "w") as f:
    json.dump({
        "plugins": {"enabled": sorted({manifest_key(m) for m in manifests})},
        "security": {"allow_lazy_installs": False},
    }, f)

import model_tools  # noqa: E402  (imports tools/*.py and discovers the plugins)
import toolsets  # noqa: E402
from hermes_cli.plugins import get_plugin_manager, get_plugin_toolsets  # noqa: E402
from hermes_cli.toolset_scope import _TOOLSET_PLATFORM_RESTRICTIONS  # noqa: E402
from tools.registry import registry  # noqa: E402

names = set(toolsets.TOOLSETS) | set(registry.get_registered_toolset_names())
plugin_toolsets = {key for key, _, _ in get_plugin_toolsets()}
names |= plugin_toolsets


def disable_removes(name):
    """What `agent.disabled_toolsets: [name]` subtracts
    (model_tools._apply_toolset_selection)."""
    spec = toolsets.get_toolset(name) or {}
    if name.startswith("hermes-") or spec.get("posture"):
        return sorted(toolsets.bundle_non_core_tools(name))
    return sorted(toolsets.resolve_toolset(name))


registered = set(registry.get_all_tool_names())
plugins = []
for key, loaded in sorted(get_plugin_manager()._plugins.items()):
    declared = list(loaded.manifest.provides_tools or [])
    plugins.append({
        "key": key,
        "kind": loaded.manifest.kind,
        "error": loaded.error or "",
        "unregistered": sorted(t for t in declared if t not in registered),
    })

gated = {}
for name, platforms in toolsets.TOOLSET_SESSION_PLATFORMS.items():
    gated[name] = sorted(platforms)
for name, platforms in _TOOLSET_PLATFORM_RESTRICTIONS.items():
    gated[name] = sorted(set(gated.get(name, [])) | set(platforms))

json.dump({
    "toolsets": {n: {
        "tools": sorted(toolsets.resolve_toolset(n)),
        "disable_removes": disable_removes(n),
        "plugin": n in plugin_toolsets,
    } for n in sorted(names)},
    "aliases": registry.get_registered_toolset_aliases(),
    "legacy": sorted(model_tools._LEGACY_TOOLSET_MAP),
    "platform_gated": gated,
    "client_surface": sorted(toolsets.CLIENT_SURFACE_TOOLSETS),
    "plugins": plugins,
}, out, indent=1, sort_keys=True)
out.write("\n")
