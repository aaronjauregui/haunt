# Installing

## What it runs on

Haunt draws its ghosts as layer-shell overlay surfaces, so it needs a
Wayland compositor that implements `zwlr_layer_shell_v1`: sway, Hyprland,
and others in the wlroots family, and KDE. GNOME does not implement it and
is not supported. There is no X11 path.

Fullscreen detection and window awareness ask the compositor over its own
IPC. Sway and Hyprland are recognised from the environment. On any other
compositor the ghost still runs, but assumes nothing is fullscreen and
cannot see which window you are in.

Requirements:

| For | Needs |
|---|---|
| Building | Go 1.25 or later |
| A ghost that thinks | hermes-agent, see [The brain](hermes.md) |
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

A user unit gets a sparse `PATH`. If `hermes` lives somewhere like
`~/.local/bin`, name it in the config:

```toml
[hermes]
command = ["/home/you/.local/bin/hermes"]
```

## Where things live

| What | Where |
|---|---|
| Config | `~/.config/haunt/config.toml` (`XDG_CONFIG_HOME` respected; `HAUNT_CONFIG_DIR` overrides) |
| Installed ghosts | `~/.local/share/haunt/ghosts/<name>/` (`XDG_DATA_HOME` respected) |
| A ghost's memory | `ghosts/<name>/state.db`, its grants in `ghosts/<name>/grants.toml` |
| Who is resident | `~/.local/share/haunt/resident` |
| Control sockets | under `XDG_RUNTIME_DIR` (`HAUNT_RUNTIME_DIR` overrides) |
| Author signing key | see [Packaging](../ghosts/packaging.md) |
| A ghost's hermes profile | `~/.hermes/profiles/haunt-<name>/` (`[hermes] home` or `HERMES_HOME` overrides) |

Nothing in the config file is required. A missing file is the shipping
configuration; [Configuration](config.md) lists what can go in it.
