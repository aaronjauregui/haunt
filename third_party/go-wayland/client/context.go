package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

// HAUNT PATCH: the upstream Context has no synchronization, but Haunt issues
// requests (which Register/Unregister proxies) from goroutines other than
// the dispatch loop. objectsMu guards the proxy table; writeMu (used in
// request.go) serializes request writes. See README.haunt.md.

type Context struct {
	conn      *net.UnixConn
	objects   map[uint32]Proxy
	currentID uint32

	objectsMu sync.RWMutex
	writeMu   sync.Mutex
}

func (ctx *Context) Register(p Proxy) {
	ctx.objectsMu.Lock()
	defer ctx.objectsMu.Unlock()
	ctx.currentID++
	p.SetID(ctx.currentID)
	p.SetContext(ctx)
	ctx.objects[ctx.currentID] = p
}

func (ctx *Context) Unregister(p Proxy) {
	ctx.objectsMu.Lock()
	defer ctx.objectsMu.Unlock()
	delete(ctx.objects, p.ID())
}

func (ctx *Context) GetProxy(id uint32) Proxy {
	ctx.objectsMu.RLock()
	defer ctx.objectsMu.RUnlock()
	return ctx.objects[id]
}

func (ctx *Context) Close() error {
	return ctx.conn.Close()
}

func (ctx *Context) Dispatch() error {
	senderID, opcode, fd, data, err := ctx.ReadMsg()
	if err != nil {
		return fmt.Errorf("ctx.Dispatch: unable to read msg: %w", err)
	}

	// Look up under the read lock but invoke the handler outside it:
	// handlers issue requests, which Register proxies (write lock).
	ctx.objectsMu.RLock()
	sender, ok := ctx.objects[senderID]
	ctx.objectsMu.RUnlock()
	if ok {
		if sender, ok := sender.(Dispatcher); ok {
			sender.Dispatch(opcode, fd, data)
		} else {
			return fmt.Errorf("ctx.Dispatch: sender doesn't implement Dispatch method (senderID=%d)", senderID)
		}
	} else {
		return fmt.Errorf("ctx.Dispatch: unable find sender (senderID=%d)", senderID)
	}

	return nil
}

func Connect(addr string) (*Display, error) {
	if addr == "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			return nil, errors.New("env XDG_RUNTIME_DIR not set")
		}
		if addr == "" {
			addr = os.Getenv("WAYLAND_DISPLAY")
		}
		if addr == "" {
			addr = "wayland-0"
		}
		addr = runtimeDir + "/" + addr
	}

	ctx := &Context{
		objects: map[uint32]Proxy{},
	}

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: addr, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ctx.conn = conn

	return NewDisplay(ctx), nil
}
