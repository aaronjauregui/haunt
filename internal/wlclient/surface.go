package wlclient

import (
	"errors"
	"fmt"
	"image"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"golang.org/x/sys/unix"

	"github.com/rajveermalviya/go-wayland/wayland/stable/viewporter"
	fractionalscale "github.com/rajveermalviya/go-wayland/wayland/staging/fraction-sclae-v1"

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
	// position. OnMove fires for every position change during a drag so
	// attached surfaces (balloons) can follow. All are called from the
	// dispatch goroutine, outside the app lock.
	OnClick   func(x, y int)
	OnDragEnd func(x, y int)
	OnMove    func(x, y int)

	// OnRightClick fires on a right button press over an opaque pixel: the
	// gesture for addressing the ghost directly.
	OnRightClick func()

	// OnScroll reports the wheel turned over the surface, in notches:
	// negative is up (away from the user), positive down. A touchpad's
	// continuous scroll arrives in notch-sized steps. OnHover reports the
	// pointer arriving on the surface and leaving it. Both are called from
	// the dispatch goroutine, outside the app lock.
	OnScroll func(steps int)
	OnHover  func(inside bool)

	// OnScaleChanged reports the compositor's preferred scale for this
	// surface (1.0, 1.5, 2.0…), so the caller can redraw its art at that
	// resolution. Fires on the dispatch goroutine, outside the app lock.
	OnScaleChanged func(scale float64)

	// OnKey receives raw Linux scancodes while this surface holds keyboard
	// focus (see App.TakeKeyboard); down distinguishes press from release.
	// OnKeyboardFocus reports gaining and losing that focus — losing it is
	// how an input line learns the user's attention went elsewhere.
	OnKey           func(code uint32, down bool)
	OnKeyboardFocus func(focused bool)

	// KeyRepeats says whether holding a key repeats it, at the rate the
	// compositor asks for; repeated presses arrive through OnKey, from
	// another goroutine. Nil means no key repeats.
	KeyRepeats func(code uint32) bool

	app          *App
	wlSurface    *client.Surface
	layerSurface *layershell.LayerSurface
	viewport     *viewporter.Viewport
	fracScale    *fractionalscale.FractionalScale
	// scale is the compositor's preferred scale as 120ths, the unit the
	// fractional-scale protocol uses: 120 is 1x, 180 is 1.5x, 240 is 2x.
	scale   int32
	buffer  *client.Buffer
	shmData []byte
	src     *sprite.Sprite

	configured bool
	destroyed  bool
	mapFailed  error
	opts       SurfaceOptions

	pointerX, pointerY       float64
	pressX, pressY           float64
	grabX, grabY             float64
	dragOriginX, dragOriginY int32
	dragging                 bool
	// scrollAcc is continuous scroll not yet amounting to a notch.
	scrollAcc float64
}

// SurfaceOptions control how a sprite surface behaves.
type SurfaceOptions struct {
	// PassThrough gives the surface an empty input region: all pointer
	// input falls through to whatever is beneath (the usual balloon).
	PassThrough bool
	// Fixed takes clicks but never moves: a menu the user is choosing from
	// should not slide across the screen because they pressed a little
	// sideways. The ghost's own sprite is the draggable one.
	Fixed bool
	// Output pins the surface to a named screen. Empty lets the compositor
	// choose, which is the right default for a first run and the wrong one
	// for a ghost that was somewhere specific yesterday.
	Output string
	// LogicalW, LogicalH are the size the buffer stands for, when it was
	// drawn at a HiDPI scale and is therefore bigger than that. Zero means
	// the buffer is already logical size.
	LogicalW, LogicalH int32
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
	// Logged because surface churn is a bug with a look: content that
	// animates by recreating its surface flickers, and counting these is
	// how a test tells the difference.
	a.logEvent("surface create %dx%d at (%d,%d)", w, h, x, y)
	logicalW, logicalH := int32(w), int32(h)
	if opts.LogicalW > 0 && opts.LogicalH > 0 {
		logicalW, logicalH = opts.LogicalW, opts.LogicalH
	}
	s := &SpriteSurface{X: x, Y: y, W: logicalW, H: logicalH, app: a, opts: opts, src: sp}

	a.mu.Lock()
	defer a.mu.Unlock()

	var err error
	if s.wlSurface, err = a.compositor.CreateSurface(); err != nil {
		return nil, err
	}
	// HiDPI: ask the compositor what scale it wants this surface drawn at,
	// and put a viewport on it. Without this the ghost is rendered at
	// logical size and the compositor stretches it — a 160px sprite on a
	// 200% screen is either half the size it should be or twice as blurry.
	// With it, the buffer is drawn at the real device resolution and the
	// viewport declares the logical size it stands for, which is also how
	// non-integer scales like 1.25 are expressed at all.
	if a.fracScale != nil {
		if fs, err := a.fracScale.GetFractionalScale(s.wlSurface); err == nil {
			s.fracScale = fs
			fs.SetPreferredScaleHandler(func(e fractionalscale.FractionalScalePreferredScaleEvent) {
				a.mu.Lock()
				changed := int32(e.Scale) != s.scale
				s.scale = int32(e.Scale)
				a.mu.Unlock()
				if changed && s.OnScaleChanged != nil {
					s.OnScaleChanged(float64(e.Scale) / 120)
				}
			})
		}
	}
	if a.viewporter != nil {
		if vp, err := a.viewporter.GetViewport(s.wlSurface); err == nil {
			s.viewport = vp
		}
	}

	// Layer-shell coordinates are relative to the output the surface is
	// on, so pinning the output is what makes a remembered position mean
	// the same thing tomorrow.
	var wlOut *client.Output
	if opts.Output != "" {
		for _, o := range a.outputs {
			if o.Name == opts.Output {
				wlOut = o.wl
				break
			}
		}
		if wlOut == nil {
			a.logEvent("output %q is not connected; letting the compositor choose", opts.Output)
		}
	}
	if s.layerSurface, err = a.layerShell.GetLayerSurface(s.wlSurface, wlOut, LayerOverlay, "haunt"); err != nil {
		return nil, err
	}

	s.layerSurface.SetConfigureHandler(func(e layershell.LayerSurfaceConfigureEvent) {
		// Runs on the dispatch goroutine. The surface may have been
		// destroyed between the event's dispatch lookup and this handler
		// acquiring the lock; acking a destroyed object is a protocol
		// error, so re-check under the lock.
		a.mu.Lock()
		defer a.mu.Unlock()
		if s.destroyed {
			return
		}
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

	// The layer surface is sized in logical pixels; the buffer may be
	// larger when drawn for a HiDPI screen.
	s.layerSurface.SetSize(uint32(s.W), uint32(s.H))
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
	// The buffer may be bigger than the logical size when the surface is
	// drawn for a HiDPI screen; the viewport says what it stands for.
	if err := s.setViewport(w, h); err != nil {
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

// Geometry returns the surface's current position and size. Safe from any
// goroutine.
func (s *SpriteSurface) Geometry() (x, y, w, h int32) {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	return s.X, s.Y, s.W, s.H
}

// MoveTo repositions the sprite via its layer-surface margins. Safe from any
// goroutine.
func (s *SpriteSurface) MoveTo(x, y int32) error {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if s.destroyed {
		return nil
	}
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
	if s.destroyed {
		return nil
	}
	s.destroyed = true
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

// ErrSizeChanged says a redraw cannot happen in place because the new
// content is a different size; the caller must recreate the surface.
var ErrSizeChanged = errors.New("sprite size differs from the surface")

// Redraw replaces a surface's pixels in place, for content that changes
// without changing size: a blinking caret, a line of text as it is typed.
// Destroying and recreating a layer surface per frame is what makes a
// balloon flicker, so anything animating should come through here.
//
// The pixels are written into the buffer the compositor already has rather
// than into a fresh one. For a small text panel that is worth the sliver of
// a frame in which the compositor could read a half-written buffer; the
// principled fix, if it ever shows, is a second buffer swapped on
// wl_buffer.release.
func (s *SpriteSurface) Redraw(sp *sprite.Sprite) error {
	return s.RedrawRect(sp, sp.Img.Bounds())
}

// RedrawRect is Redraw for a known-changed region: only those pixels are
// written and only that rectangle is damaged, so the compositor re-reads a
// blinking eye rather than a whole ghost.
func (s *SpriteSurface) RedrawRect(sp *sprite.Sprite, r image.Rectangle) error {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if s.destroyed {
		return nil
	}
	w, h := sp.Size()
	if int32(w) != s.W || int32(h) != s.H || s.shmData == nil || s.buffer == nil {
		return ErrSizeChanged
	}
	r = r.Intersect(sp.Img.Bounds())
	if r.Empty() {
		return nil
	}
	sp.WriteBGRARect(s.shmData, r)
	s.src = sp
	if err := s.wlSurface.Attach(s.buffer, 0, 0); err != nil {
		return err
	}
	if err := s.wlSurface.Damage(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil {
		return err
	}
	return s.wlSurface.Commit()
}

// Scale is the compositor's preferred scale for this surface (1.0 when it
// has not said, or when the protocol is missing).
func (s *SpriteSurface) Scale() float64 {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if s.scale <= 0 {
		return 1
	}
	return float64(s.scale) / 120
}

// setViewport declares the logical size a buffer stands for. Callers hold
// the app lock.
func (s *SpriteSurface) setViewport(bufW, bufH int) error {
	if s.viewport == nil {
		return nil
	}
	w, h := int32(bufW), int32(bufH)
	if s.W > 0 && s.H > 0 {
		w, h = s.W, s.H
	}
	return s.viewport.SetDestination(w, h)
}
