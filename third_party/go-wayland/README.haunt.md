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

`client/context.go`, `client/request.go` and `client/client.go` are
modified, marked with `HAUNT PATCH` comments:

- `Context.objectsMu` (RWMutex) guards the proxy table in
  Register/Unregister/GetProxy/Dispatch. Dispatch looks up the sender under
  the read lock but invokes the handler outside it, since handlers issue
  requests that take the write lock.
- `Context.writeMu` serializes `WriteMsg`, so concurrent request writers
  cannot interleave bytes on the socket.
- Event decoding asserts object arguments with a comma-ok rather than a
  plain type assertion (11 sites in `client.go`, all of the form
  `e.X = ctx.GetProxy(id).(*T)`). An event can name an object the client
  has already destroyed — the destroy request and the event crossed on the
  wire — and the proxy table has forgotten it, so upstream panics on a nil
  proxy. A client must not die because it tidied up a moment before the
  server said something about it. Reproduced by a balloon that takes
  pointer input: destroying it while the pointer is inside earns a
  `wl_pointer.leave` for a dead surface, every time.

Everything else is byte-for-byte upstream. If upstream ever gains its own
locking, drop this directory and the replace directive.
