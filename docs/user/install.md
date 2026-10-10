# Installing

## What it runs on

Haunt draws its ghosts as layer-shell overlay surfaces, so it needs a
Wayland compositor that implements `zwlr_layer_shell_v1`: sway, Hyprland,
and others in the wlroots family, and KDE. GNOME does not implement it and
is not supported. There is no X11 path.

A compositor that cannot keep a ghost can still show you one. The demos
(`make demo`, and the rest in the Makefile) run sway nested inside
whatever session is already running, which is enough to try a ghost
without changing compositor; `WLR_BACKENDS=x11 ./scripts/demo.sh` does
the same from an X11 session. Nothing about that path is suitable for
daily use: a nested compositor is a window, not a desktop.

Fullscreen detection and window awareness ask the compositor over its own
IPC. Sway and Hyprland are recognised from the environment. On any other
compositor the ghost still runs, but assumes nothing is fullscreen and
cannot see which window you are in.

Requirements:

| For | Needs |
|---|---|
| Building | Go 1.25 or later |
| A ghost that thinks | an OpenAI-compatible model endpoint, local or cloud, see [The model](model.md) |
| A ghost that looks at your screen | `grim` |
| Running the tests | `sway` and `grim`, and `foot` for the window tests |

## Building and installing

    make build            # ./haunt and ./haunt-seriko
    make install-user     # binaries into ~/.local/bin, unit into ~/.config/systemd/user
    systemctl --user enable --now haunt

The unit is wanted by `graphical-session.target`, so the resident ghosts
appear when you log into a Wayland session and go away when you log out.
The resident set is read at startup: after `haunt add` or `haunt drop`,
`systemctl --user restart haunt` applies the change to a session that is
already running.

Sway and Hyprland do not always populate `graphical-session.target` on
their own. If the service never starts, add to your compositor config:

    # sway
    exec "systemctl --user import-environment WAYLAND_DISPLAY XDG_CURRENT_DESKTOP; \
          systemctl --user start graphical-session.target"

Logs, including each ghost's own:

    journalctl --user -u haunt -f

A user unit gets a sparse environment. A cloud model's key, named by
`[model] api_key_env`, has to be in it:

    systemctl --user set-environment ZAI_API_KEY=…

or an `Environment=` line in a drop-in made with `systemctl --user edit
haunt`.

## Where things live

| What | Where |
|---|---|
| Config | `~/.config/haunt/config.toml` (`XDG_CONFIG_HOME` respected; `HAUNT_CONFIG_DIR` overrides) |
| Installed ghosts | `~/.local/share/haunt/ghosts/<name>/` (`XDG_DATA_HOME` respected) |
| A ghost's state | `ghosts/<name>/state.db`, its memory in `memory/`, its conversation in `transcript.db`, its grants in `grants.toml` |
| Who is resident | `~/.local/share/haunt/resident` |
| Control sockets | under `XDG_RUNTIME_DIR` (`HAUNT_RUNTIME_DIR` overrides) |
| Author signing key | see [Packaging](../ghosts/packaging.md) |

Nothing in the config file is required. A missing file is the shipping
configuration; [Configuration](config.md) lists what can go in it.
