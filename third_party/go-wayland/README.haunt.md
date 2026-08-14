# Patched go-wayland

This is a vendored copy of github.com/rajveermalviya/go-wayland/wayland
(v0.0.0-20230130181619-0ad78d1310b2, upstream license unchanged in LICENSE),
wired in via a `replace` directive in the root go.mod.

## Why

Upstream `client.Context` has no synchronization. Haunt issues Wayland
requests from goroutines other than the dispatch loop (brain intents create
and destroy balloon surfaces), and every request registers/unregisters
proxies in `Context.objects` while `Dispatch` reads that map concurrently —
a fatal "concurrent map read and map write" under load (reproduced by
rapidly clicking the sprite while the brain swaps balloons).

## The patch

Only `client/context.go` and `client/request.go` are modified, marked with
`HAUNT PATCH` comments:

- `Context.objectsMu` (RWMutex) guards the proxy table in
  Register/Unregister/GetProxy/Dispatch. Dispatch looks up the sender under
  the read lock but invokes the handler outside it, since handlers issue
  requests that take the write lock.
- `Context.writeMu` serializes `WriteMsg`, so concurrent request writers
  cannot interleave bytes on the socket.

Everything else is byte-for-byte upstream. If upstream ever gains its own
locking, drop this directory and the replace directive.
