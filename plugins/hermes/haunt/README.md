# haunt memory provider for hermes

A hermes-agent memory provider that makes a running haunt ghost's belief
store hermes's view of the people in the person's life:

- each turn, `prefetch` asks the ghost for its standing beliefs about the
  people the message names and adds them to the turn's context;
- `beliefs` lists standing beliefs (with fact ids) for a subject, or for
  everyone;
- `correct` proposes a change from the person's own words. The ghost asks
  the person on screen and applies the change only on yes, so the call can
  take minutes.

The provider only talks to the ghost's control socket
(`$HAUNT_RUNTIME_DIR/<ghost>.sock`, else `$XDG_RUNTIME_DIR/haunt/<ghost>.sock`,
else `$TMPDIR/haunt/<ghost>.sock`). It never opens the ghost's database: the
ghost is the only writer. `sync_turn` and `on_memory_write` do nothing, and
hermes's own `MEMORY.md`/`USER.md` stay hermes's. If the ghost is not
running, recall is skipped and the tools return an error message.

## Install

Haunt copies `__init__.py` and `plugin.yaml` (not the test file: hermes
runs every `*.py` in a plugin directory) into the ghost's profile as
`$HERMES_HOME/plugins/haunt/`, sets `HAUNT_GHOST=<ghost>` in the profile's
`.env`, and selects the provider in the profile's `config.yaml`:

```yaml
memory:
  provider: haunt
```

Without `HAUNT_GHOST`, the ghost name is the profile directory's name with a
leading `haunt-` removed. Hermes only offers memory-provider tools when the
`memory` toolset is enabled for the platform.

## Tests

```sh
python -m unittest discover plugins/hermes/haunt
```

using a python with hermes-agent installed. The tests run a fake control
socket, and load the plugin through hermes's own discovery from a temporary
`$HERMES_HOME`.
