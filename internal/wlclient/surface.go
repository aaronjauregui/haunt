package wlclient

import (
	"fmt"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"golang.org/x/sys/unix"

	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/wayland/layershell"
)

// SpriteSurface is a sprite rendered as a layer-shell overlay surface,
// positioned by top/left margins and draggable with the pointer.
type SpriteSurface struct {
	X, Y int32
	W, H int32

	// OnClick fires on press+release without movement, with surface-local
	// coordinates. OnDragEnd fires when a drag settles, with the new
	// position. Both are called from the dispatch goroutine.
	OnClick   func(x, y int)
	OnDragEnd func(x, y int)

	app          *App
	wlSurface    *client.Surface
	layerSurface *layershell.LayerSurface
	buffer       *client.Buffer
	shmData      []byte
	src          *sprite.Sprite

	configured bool
	mapFailed  error
	opts       SurfaceOptions

	pointerX, pointerY       float64
	grabX, grabY             float64
	dragOriginX, dragOriginY int32
	dragging                 bool
}

// SurfaceOptions control how a sprite surface behaves.
type SurfaceOptions struct {
	// PassThrough gives the surface an empty input region: all pointer
	// input falls through to whatever is beneath (used by balloons).
	PassThrough bool
}

// CreateSprite maps sp as an overlay at position (x, y). Mapping completes
// asynchronously when the compositor sends the initial configure, which the
// dispatch goroutine handles; use WaitMapped to block for it during
// single-threaded startup.
func (a *App) CreateSprite(sp *sprite.Sprite, x, y int32) (*SpriteSurface, error) {
	return a.CreateSpriteOpts(sp, x, y, SurfaceOptions{})
}

// CreateSpriteOpts is CreateSprite with explicit options. Safe to call from
// any goroutine while the dispatch loop runs.
func (a *App) CreateSpriteOpts(sp *sprite.Sprite, x, y int32, opts SurfaceOptions) (*SpriteSurface, error) {
	w, h := sp.Size()
	s := &SpriteSurface{X: x, Y: y, W: int32(w), H: int32(h), app: a, opts: opts, src: sp}

	a.mu.Lock()
	defer a.mu.Unlock()

	var err error
	if s.wlSurface, err = a.compositor.CreateSurface(); err != nil {
		return nil, err
	}
	if s.layerSurface, err = a.layerShell.GetLayerSurface(s.wlSurface, nil, LayerOverlay, "haunt"); err != nil {
		return nil, err
	}

	s.layerSurface.SetConfigureHandler(func(e layershell.LayerSurfaceConfigureEvent) {
		// Runs on the dispatch goroutine.
		a.mu.Lock()
		defer a.mu.Unlock()
		s.layerSurface.AckConfigure(e.Serial)
		if s.configured {
			return
		}
		if err := s.completeMapping(); err != nil {
			s.mapFailed = err
			return
		}
		s.configured = true
	})
	s.layerSurface.SetClosedHandler(func(layershell.LayerSurfaceClosedEvent) {
		s.configured = false
	})

	s.layerSurface.SetSize(uint32(w), uint32(h))
	s.layerSurface.SetAnchor(AnchorTop | AnchorLeft)
	s.layerSurface.SetMargin(y, 0, 0, x)
	s.layerSurface.SetKeyboardInteractivity(KeyboardInteractivityNone)
	if err := s.wlSurface.Commit(); err != nil {
		return nil, err
	}
	a.surfaces[s.wlSurface.ID()] = s
	return s, nil
}

// completeMapping attaches pixels and input region after the initial
// configure. Called with a.mu held, on the dispatch goroutine.
func (s *SpriteSurface) completeMapping() error {
	if err := s.attach(s.src); err != nil {
		return err
	}
	if err := s.setInputRegion(s.src); err != nil {
		return err
	}
	return s.wlSurface.Commit()
}

// WaitMapped pumps the event queue until the surface is mapped. Only for
// single-threaded startup, before the dispatch loop goroutine exists.
func (s *SpriteSurface) WaitMapped() error {
	for !s.configured {
		if s.mapFailed != nil {
			return s.mapFailed
		}
		if err := s.app.Dispatch(); err != nil {
			return fmt.Errorf("waiting for layer surface configure: %w", err)
		}
	}
	return nil
}

// attach uploads the sprite pixels into a shm buffer and attaches it.
func (s *SpriteSurface) attach(sp *sprite.Sprite) error {
	w, h := sp.Size()
	stride := w * 4
	size := stride * h

	fd, err := unix.MemfdCreate("haunt-shm", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("memfd_create: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return fmt.Errorf("ftruncate: %w", err)
	}
	data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap: %w", err)
	}
	s.shmData = data
	sp.WriteBGRA(data)

	pool, err := s.app.shm.CreatePool(fd, int32(size))
	if err != nil {
		return err
	}
	const formatARGB8888 = 0
	buf, err := pool.CreateBuffer(0, int32(w), int32(h), int32(stride), formatARGB8888)
	if err != nil {
		return err
	}
	pool.Destroy()
	s.buffer = buf

	if err := s.wlSurface.Attach(buf, 0, 0); err != nil {
		return err
	}
	return s.wlSurface.Damage(0, 0, int32(w), int32(h))
}

// setInputRegion restricts pointer input to the sprite's opaque pixels, or
// to nothing at all for pass-through surfaces.
func (s *SpriteSurface) setInputRegion(sp *sprite.Sprite) error {
	const alphaThreshold = 16
	region, err := s.app.compositor.CreateRegion()
	if err != nil {
		return err
	}
	if !s.opts.PassThrough {
		for _, r := range sp.OpaqueRects(alphaThreshold) {
			if err := region.Add(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil {
				return err
			}
		}
	}
	if err := s.wlSurface.SetInputRegion(region); err != nil {
		return err
	}
	return region.Destroy()
}

// MoveTo repositions the sprite via its layer-surface margins. Safe from any
// goroutine.
func (s *SpriteSurface) MoveTo(x, y int32) error {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	s.X, s.Y = x, y
	if err := s.layerSurface.SetMargin(y, 0, 0, x); err != nil {
		return err
	}
	return s.wlSurface.Commit()
}

// Destroy removes the surface and releases its buffer memory. Safe from any
// goroutine.
func (s *SpriteSurface) Destroy() error {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	delete(s.app.surfaces, s.wlSurface.ID())
	if s.app.pointerFocus == s {
		s.app.pointerFocus = nil
	}
	s.layerSurface.Destroy()
	s.wlSurface.Destroy()
	if s.buffer != nil {
		s.buffer.Destroy()
	}
	if s.shmData != nil {
		unix.Munmap(s.shmData)
		s.shmData = nil
	}
	return nil
}
