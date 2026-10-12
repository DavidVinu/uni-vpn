package devsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"rsc.io/qr"
)

// Messages in the two pairing slots (docs/sync.md, Pairing).
type pairMessage struct {
	Kind string `json:"kind"`           // a: "offer" or "request"; b: "joined" or "root"
	Root []byte `json:"root,omitempty"` // offer, root
}

func (e *Engine) putSlot(ctx context.Context, p pairing, slot string, m pairMessage) error {
	id := p.a
	if slot == "b" {
		id = p.b
	}
	return e.relay.putPair(ctx, id, seal(p.key, "pairing "+slot, mustJSON(m)))
}

func (e *Engine) takeSlot(ctx context.Context, p pairing, slot string) (pairMessage, error) {
	id := p.a
	if slot == "b" {
		id = p.b
	}
	blob, err := e.relay.takePair(ctx, id)
	if err != nil {
		return pairMessage{}, err
	}
	plain, err := open(p.key, "pairing "+slot, blob)
	if err != nil {
		return pairMessage{}, err
	}
	var m pairMessage
	if err := json.Unmarshal(plain, &m); err != nil {
		return pairMessage{}, errDecrypt
	}
	return m, nil
}

// ensureGroup makes this device the first of a new group when it is in none yet.
func (e *Engine) ensureGroup(ctx context.Context) (*keys, error) {
	for range 2 {
		k, err := e.recheck(ctx)
		if err != nil {
			return nil, err
		}
		if k == nil {
			k = newKeys(newGroup().root, "", nil)
			if err := e.saveKeys(k); err != nil {
				return nil, err
			}
			if err := e.saveState(state{Keyed: true, Hashes: map[string]savedHash{}}); err != nil {
				return nil, err
			}
			e.logf(slog.LevelInfo, "Syncing turned on")
		}
		if err := e.Sync(ctx); err != nil && !partly(err) {
			return nil, err
		}
		// The round may have moved this device to the new key of a removal made elsewhere, or
		// out of its group: the code hands out the key it has now.
		if k, err = e.loadKeys(ctx); err != nil || k != nil {
			return k, err
		}
	}
	return nil, errConflict
}

// join puts this device into the group of root and takes the group's settings.
func (e *Engine) join(ctx context.Context, root []byte) error {
	if len(root) != 32 {
		return errDecrypt
	}
	device, priv := "", []byte(nil)
	if k, _ := e.loadKeys(ctx); k != nil {
		if string(k.Root) == string(root) {
			return nil // already in this group
		}
		device, priv = k.Device, k.Priv
	}
	if err := e.saveKeys(newKeys(root, device, priv)); err != nil {
		return err
	}
	if err := e.saveState(state{Adopt: true, Keyed: true, Hashes: map[string]savedHash{}}); err != nil {
		return err
	}
	e.logf(slog.LevelInfo, "Joined the settings of another device")
	err := e.Sync(ctx)
	if k, _ := e.loadKeys(ctx); k == nil {
		// The group has moved on without this device: the code was shown before a removal.
		return ErrCodeUnknown
	}
	return err
}

// Offer shows a code another device enters or scans to get this device's settings
// ("Add a device").
func (e *Engine) Offer(ctx context.Context) (Pairing, error) {
	if e.relay == nil {
		return Pairing{}, ErrUnavailable
	}
	if !e.opts.Local.SetupDone() {
		return Pairing{}, ErrSetupFirst
	}
	k, err := e.ensureGroup(ctx)
	if err != nil {
		return Pairing{}, offline(err)
	}
	code := newCode()
	p := pairingFor(code)
	if err := e.putSlot(ctx, p, "a", pairMessage{Kind: "offer", Root: k.Root}); err != nil {
		return Pairing{}, offline(err)
	}
	return e.show("offer", code, p), nil
}

// Request shows a code for a device that already has the settings: it scans or types it
// and sends them here ("Use settings from another device").
func (e *Engine) Request(ctx context.Context) (Pairing, error) {
	if e.relay == nil {
		return Pairing{}, ErrUnavailable
	}
	code := newCode()
	p := pairingFor(code)
	if err := e.putSlot(ctx, p, "a", pairMessage{Kind: "request"}); err != nil {
		return Pairing{}, offline(err)
	}
	return e.show("request", code, p), nil
}

// show puts the code on screen and waits in the background for the other device's answer.
func (e *Engine) show(mode, code string, p pairing) Pairing {
	shown := Pairing{Mode: mode, Code: FormatCode(code), QR: QRSVG(QRPrefix + code), State: "waiting"}
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.CodeLifetime)
	e.cancelPairing() // a new code replaces the one on screen
	e.mu.Lock()
	e.pairing, e.cancelPair, e.pairSlot = &shown, cancel, p.a
	e.mu.Unlock()
	go e.await(ctx, cancel, &shown, p)
	return shown
}

func (e *Engine) await(ctx context.Context, cancel context.CancelFunc, shown *Pairing, p pairing) {
	defer cancel()
	finish := func(st string, err error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.pairing == shown {
			done := *shown
			done.State, done.Error = st, err
			e.pairing = &done
		}
	}
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			finish("expired", nil)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.opts.PollEvery):
		}
		m, err := e.takeSlot(ctx, p, "b")
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errNotFound) || (err != nil && !errors.Is(err, errDecrypt)) {
			continue // not yet, or the network hiccuped
		}
		if err != nil {
			finish("failed", err)
			return
		}
		switch {
		case shown.Mode == "offer" && m.Kind == "joined":
			e.logf(slog.LevelInfo, "Another device now syncs with this one")
			finish("done", nil)
			go func() {
				if err := e.Sync(context.Background()); err != nil {
					e.logf(slog.LevelWarn, "Sync failed: %s", err)
				}
			}()
		case shown.Mode == "request" && m.Kind == "root":
			err := e.join(context.WithoutCancel(ctx), m.Root)
			var ae *ApplyError
			if err != nil && !errors.As(err, &ae) {
				finish("failed", offline(err))
				return
			}
			finish("done", err) // joined; err says what could not be taken over
		default:
			finish("failed", errDecrypt)
		}
		return
	}
}

// Enter is the other side: the code was typed, or scanned from the other device's screen.
func (e *Engine) Enter(ctx context.Context, text string) error {
	if e.relay == nil {
		return ErrUnavailable
	}
	code, ok := NormalizeCode(text)
	if !ok {
		return ErrCodeUnknown
	}
	p := pairingFor(code)
	m, err := e.takeSlot(ctx, p, "a")
	if errors.Is(err, errNotFound) || errors.Is(err, errDecrypt) {
		return ErrCodeUnknown
	}
	if err != nil {
		return offline(err)
	}
	switch m.Kind {
	case "offer":
		err := e.join(ctx, m.Root)
		var ae *ApplyError
		if err != nil && !errors.As(err, &ae) {
			return offline(err)
		}
		if err := e.putSlot(ctx, p, "b", pairMessage{Kind: "joined"}); err != nil {
			return offline(err)
		}
		return err // joined; an *ApplyError says what could not be taken over
	case "request":
		if !e.opts.Local.SetupDone() {
			return ErrSetupFirst
		}
		k, err := e.ensureGroup(ctx)
		if err != nil {
			return offline(err)
		}
		if err := e.putSlot(ctx, p, "b", pairMessage{Kind: "root", Root: k.Root}); err != nil {
			return offline(err)
		}
		e.logf(slog.LevelInfo, "Sent the settings to another device")
		go func() {
			// The new device adds itself to the list in a moment.
			time.Sleep(15 * time.Second)
			e.Wake()
		}()
		return nil
	}
	return ErrCodeUnknown
}

// CancelPairing takes the code off the screen.
func (e *Engine) CancelPairing() { e.cancelPairing() }

func (e *Engine) cancelPairing() {
	e.mu.Lock()
	waiting := e.pairing != nil && e.pairing.State == "waiting"
	if e.cancelPair != nil {
		e.cancelPair()
	}
	slot := e.pairSlot
	e.pairing, e.cancelPair, e.pairSlot = nil, nil, ""
	e.mu.Unlock()
	if waiting && slot != "" {
		// Reading the slot empties it: a code someone saw no longer works.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := e.relay.takePair(ctx, slot); err != nil && !errors.Is(err, errNotFound) {
			e.logf(slog.LevelWarn, "Could not withdraw the code: %s", err)
		}
	}
}

// offline keeps the errors the app explains and calls everything else unreachable.
func offline(err error) error {
	var ae *ApplyError
	if err == nil || errors.Is(err, ErrCodeUnknown) || errors.Is(err, ErrSetupFirst) || errors.Is(err, ErrUnavailable) ||
		errors.As(err, &ae) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrOffline, err)
}

// QRSVG draws text as a QR code: black squares on white with the standard quiet zone.
func QRSVG(text string) string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	n := code.Size + 8
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`, n, n, n, n, path.String())
}
