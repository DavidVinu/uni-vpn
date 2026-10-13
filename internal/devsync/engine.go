package devsync

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Local is the device's own settings: what the daemon has in config.toml, the website list
// and the keyring.
type Local interface {
	// Snapshot is every value by document key; without the secrets unless withSecrets.
	Snapshot(ctx context.Context, withSecrets bool) (map[string]json.RawMessage, error)
	// Apply changes the listed keys to their values in all (the whole merged document) and
	// returns the keys it could not change, with the reason.
	Apply(ctx context.Context, all map[string]Entry, keys []string) map[string]error
	// SetupDone: the device has an account to share.
	SetupDone() bool
}

// Store keeps this device's sync keys in the keyring as one line; Load returns "" when
// there is none.
type Store interface {
	Load(ctx context.Context) (string, error)
	Save(value string) error
	Delete() error
}

var (
	// ErrUnavailable: no relay is configured in this build.
	ErrUnavailable = errors.New("syncing is not available")
	// ErrCodeUnknown: nobody shows this code (mistyped, used or expired).
	ErrCodeUnknown = errors.New("unknown or expired code")
	// ErrSetupFirst: neither device has an account to share yet.
	ErrSetupFirst = errors.New("set up uni-vpn first")
	// ErrOffline: the relay could not be reached.
	ErrOffline = errors.New("the sync service could not be reached")
)

// Options configure an Engine; zero values are the defaults.
type Options struct {
	Relay     string // base URL, "" turns syncing off
	StatePath string
	Store     Store
	Local     Local
	Name      string // this device's name in the list
	Platform  string // linux, macos, windows, ios, android
	Log       *slog.Logger
	Now       func() time.Time
	// Interval between background syncs; the app's page also asks for one when it opens.
	Interval time.Duration
	// PollEvery is how often a shown code checks for the other device.
	PollEvery time.Duration
	// CodeLifetime: a shown code stops working after this.
	CodeLifetime time.Duration
}

// keys is the Store's content.
type keys struct {
	Root   []byte `json:"root"`
	Device string `json:"device"`
	Priv   []byte `json:"priv"`
}

type savedHash struct {
	H string `json:"h"`
	T int64  `json:"t"`
	D string `json:"d"`
}

// state is the file next to config.toml: what was last in step, as hashes, so a local
// change is noticed without keeping any secret on disk.
type state struct {
	Version int64                `json:"version"`
	Adopt   bool                 `json:"adopt,omitempty"` // just joined: take everything from the group
	Keyed   bool                 `json:"keyed,omitempty"` // Hashes are keyedHash, not hash (older versions)
	Hashes  map[string]savedHash `json:"hashes"`
}

// Pairing is a code on screen, for the app.
type Pairing struct {
	Mode  string // "offer": this device shares its settings; "request": it receives them
	Code  string // XXXX-XXXX-XXXX
	QR    string // the code as an SVG image
	State string // "waiting", "done", "expired", "failed"
	Error error  // with "failed"
}

// DeviceInfo is one row of the device list.
type DeviceInfo struct {
	ID, Name, Platform string
	This               bool
}

// Engine syncs one device. All methods are safe for concurrent use.
type Engine struct {
	opts  Options
	relay *relay

	run sync.Mutex // one sync at a time

	mu            sync.Mutex
	keys          *keys // nil: not in a group; loaded lazily
	loaded        bool
	pending       pending
	pendingGen    int
	pendingLoaded bool
	applying      bool // Local.Apply runs: the settings it changes are not this device's own
	devices       []DeviceInfo
	lastAttempt   time.Time
	lastError     error
	pairing       *Pairing
	pairSlot      string // slot A of the code on screen
	cancelPair    context.CancelFunc
	wake          chan struct{}
}

// New builds an engine; Run starts its background loop.
func New(opts Options) *Engine {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Interval == 0 {
		opts.Interval = 30 * time.Minute
	}
	if opts.PollEvery == 0 {
		opts.PollEvery = 2 * time.Second
	}
	if opts.CodeLifetime == 0 {
		opts.CodeLifetime = 10 * time.Minute
	}
	e := &Engine{opts: opts, wake: make(chan struct{}, 1)}
	if opts.Relay != "" {
		e.relay = newRelay(opts.Relay)
	}
	return e
}

// Available: this build knows a relay.
func (e *Engine) Available() bool { return e.relay != nil }

func (e *Engine) logf(level slog.Level, format string, args ...any) {
	e.opts.Log.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

func (e *Engine) nowMS() int64 { return e.opts.Now().UnixMilli() }

// --- Keys and state ----------------------------------------------------------------

func (e *Engine) loadKeys(ctx context.Context) (*keys, error) {
	e.mu.Lock()
	if e.loaded {
		k := e.keys
		e.mu.Unlock()
		return k, nil
	}
	e.mu.Unlock()
	line, err := e.opts.Store.Load(ctx)
	if err != nil {
		return nil, err
	}
	var k *keys
	if line != "" {
		raw, err := base64.StdEncoding.DecodeString(line)
		if err == nil {
			k = &keys{}
			if json.Unmarshal(raw, k) != nil || len(k.Root) != 32 || len(k.Priv) != 32 || k.Device == "" {
				k = nil
			}
		}
		if k == nil {
			e.logf(slog.LevelWarn, "Sync keys in the keyring are unreadable, syncing is off")
		}
	}
	e.mu.Lock()
	e.keys, e.loaded = k, true
	e.mu.Unlock()
	return k, nil
}

// recheck reads the keyring again when it gave no keys before: one that did not answer at
// start (locked, its prompt dismissed) must not keep syncing off until a restart, nor let
// "Add a device" replace the keys it holds with a new group.
func (e *Engine) recheck(ctx context.Context) (*keys, error) {
	e.mu.Lock()
	if e.keys == nil {
		e.loaded = false
	}
	e.mu.Unlock()
	return e.loadKeys(ctx)
}

func (e *Engine) saveKeys(k *keys) error {
	if err := e.opts.Store.Save(base64.StdEncoding.EncodeToString(mustJSON(k))); err != nil {
		return err
	}
	e.mu.Lock()
	e.keys, e.loaded = k, true
	e.mu.Unlock()
	return nil
}

func newKeys(root []byte, device string, priv []byte) *keys {
	if device == "" {
		device = hex.EncodeToString(random(8))
	}
	if priv == nil {
		priv = random(32)
	}
	return &keys{Root: root, Device: device, Priv: priv}
}

func (k *keys) private() *ecdh.PrivateKey {
	priv, err := ecdh.X25519().NewPrivateKey(k.Priv)
	if err != nil {
		panic(err) // 32 bytes are always a valid X25519 key
	}
	return priv
}

func (k *keys) group() group { return group{root: k.Root} }

// stateKey keys the hashes in sync.json. It comes from the device's own key, which stays when
// the group moves to a new root after a removal.
func (k *keys) stateKey() []byte { return derive(k.Priv, "state hash", 32) }

func (e *Engine) loadState() state {
	var st state
	data, err := os.ReadFile(e.opts.StatePath)
	if err != nil || json.Unmarshal(data, &st) != nil {
		st = state{Keyed: true} // nothing to carry over
	}
	if st.Hashes == nil {
		st.Hashes = map[string]savedHash{}
	}
	return st
}

func (e *Engine) saveState(st state) error { return writeFile(e.opts.StatePath, mustJSON(st)) }

// writeFile replaces path in one step, readable by this user only.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// forget turns syncing off on this device: keys and state go, settings stay.
func (e *Engine) forget() {
	if err := e.opts.Store.Delete(); err != nil {
		e.logf(slog.LevelWarn, "Could not remove the sync keys: %s", err)
	}
	os.Remove(e.opts.StatePath)
	os.Remove(e.pendingPath())
	e.mu.Lock()
	e.keys, e.loaded, e.devices = nil, true, nil
	e.pending, e.pendingLoaded = pending{}, true
	e.mu.Unlock()
}

// --- Documents on the relay ---------------------------------------------------------

func groupContext(g group) string { return "group " + g.id() }

func (e *Engine) fetch(ctx context.Context, g group) (Doc, int64, error) {
	blob, version, err := e.relay.getGroup(ctx, g)
	if errors.Is(err, errNotFound) {
		return Doc{Format: Format, Entries: map[string]Entry{}}, 0, nil
	}
	if err != nil {
		return Doc{}, 0, err
	}
	plain, err := open(g.key(), groupContext(g), blob)
	if err != nil {
		return Doc{}, 0, err
	}
	var doc Doc
	if err := json.Unmarshal(plain, &doc); err != nil || doc.Format != Format {
		return Doc{}, 0, fmt.Errorf("unsupported sync data (format %d)", doc.Format)
	}
	if doc.Entries == nil {
		doc.Entries = map[string]Entry{}
	}
	return doc, version, nil
}

// store writes doc if the group is still at version (0: create it). final closes the group
// for good: after a removal, the removed device must not overwrite the forwarding note.
func (e *Engine) store(ctx context.Context, g group, version int64, doc Doc, final bool) (int64, error) {
	blob := seal(g.key(), groupContext(g), mustJSON(doc))
	if len(blob) > MaxGroupBlob {
		return 0, fmt.Errorf("sync data too large")
	}
	return e.relay.putGroup(ctx, g, version, blob, final)
}

// --- Syncing ------------------------------------------------------------------------

// pending is a local change not yet in the group, kept in a file so a restart does not lose
// it: when each value changed (it competes with other devices' changes by that time), and
// whether password or second factor changed (only then is the keyring read).
type pending struct {
	Secrets bool  `json:"secrets,omitempty"`
	At      int64 `json:"at,omitempty"` // the last change, for a value Changes does not know
	// Changes is the keyedHash and time of each changed value. Password and second factor
	// share one time and have no hash: Notify does not read the keyring.
	Changes map[string]savedHash `json:"changes,omitempty"`
}

func (e *Engine) pendingPath() string { return e.opts.StatePath + ".pending" }

// loadPendingLocked reads the file once; e.mu is held.
func (e *Engine) loadPendingLocked() {
	if e.pendingLoaded {
		return
	}
	e.pendingLoaded = true
	if data, err := os.ReadFile(e.pendingPath()); err == nil {
		json.Unmarshal(data, &e.pending)
	}
}

// Notify: a setting changed on this device; secrets says whether password or second factor
// did. The change goes out in the background.
func (e *Engine) Notify(secrets bool) {
	if e.relay == nil {
		return
	}
	e.mu.Lock()
	k, applying := e.keys, e.applying
	e.mu.Unlock()
	// A setting from another device being taken over is not a change made here. A password or
	// second factor is: Apply stores those without a Notify, so one now was typed on this device.
	if applying && !secrets {
		e.Wake()
		return
	}
	now := e.nowMS()
	// Which values changed, each with its own time: an edit made offline keeps the time it was
	// made when another one follows before the device is back online.
	var snap map[string]json.RawMessage
	st := state{Hashes: map[string]savedHash{}}
	if k != nil && e.opts.Local.SetupDone() {
		snap, _ = e.opts.Local.Snapshot(context.Background(), false)
		st = e.loadState()
	}
	e.mu.Lock()
	e.loadPendingLocked()
	p := &e.pending
	p.Secrets = p.Secrets || secrets
	p.At = now
	note := func(key, h string) {
		if p.Changes == nil {
			p.Changes = map[string]savedHash{}
		}
		p.Changes[key] = savedHash{H: h, T: now}
	}
	last := func(key string) string {
		if c, ok := p.Changes[key]; ok {
			return c.H
		}
		if old, ok := st.Hashes[key]; ok && st.Keyed {
			return old.H
		}
		return ""
	}
	if snap != nil {
		sk := k.stateKey()
		for key, v := range snap {
			if h := keyedHash(sk, v); !isSecret(key) && last(key) != h {
				note(key, h)
			}
		}
		removed := keyedHash(sk, jsonFalse)
		for _, known := range []map[string]savedHash{st.Hashes, maps.Clone(p.Changes)} {
			for key := range known {
				if _, ok := snap[key]; !ok && strings.HasPrefix(key, DomainPrefix) && last(key) != removed {
					note(key, removed)
				}
			}
		}
	}
	if secrets {
		note(KeyPassword, "")
		note(KeyTOTP, "")
	}
	e.pendingGen++
	data := mustJSON(*p)
	e.mu.Unlock()
	if err := writeFile(e.pendingPath(), data); err != nil {
		e.logf(slog.LevelWarn, "Could not note a settings change for syncing: %s", err)
	}
	e.Wake()
}

// donePending clears what was pending at gen, unless something changed since.
func (e *Engine) donePending(gen int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingGen != gen {
		return
	}
	e.pending = pending{}
	os.Remove(e.pendingPath())
}

// Wake asks the loop for a sync now.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Refresh syncs now unless that happened in the last minute; the app calls it when Settings
// opens, like Chrome sync.
func (e *Engine) Refresh(ctx context.Context) error {
	if _, err := e.recheck(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	recent := !e.lastAttempt.IsZero() && e.opts.Now().Sub(e.lastAttempt) < time.Minute
	err := e.lastError
	e.mu.Unlock()
	if recent {
		return err
	}
	return e.Sync(ctx)
}

// Run syncs on Wake and every Interval until stop is closed.
func (e *Engine) Run(stop <-chan struct{}) {
	if e.relay == nil {
		<-stop
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-stop; cancel() }()
	first := time.After(10 * time.Second)
	for {
		select {
		case <-stop:
			e.cancelPairing()
			return
		case <-first:
		case <-e.wake:
		case <-time.After(e.opts.Interval):
		}
		if err := e.Sync(ctx); err != nil && ctx.Err() == nil {
			e.logf(slog.LevelWarn, "Sync failed: %s", err)
		}
	}
}

// ApplyError: settings from the group that this device could not take over. The rest of the
// round went through; these keys are tried again when they change.
type ApplyError struct {
	Keys []string
	Err  error
}

func (e *ApplyError) Error() string {
	return fmt.Sprintf("could not apply synced %s: %s", strings.Join(e.Keys, ", "), e.Err)
}

func (e *ApplyError) Unwrap() error { return e.Err }

// partly: err only names settings this device could not take over; the round went through.
func partly(err error) bool {
	var ae *ApplyError
	return errors.As(err, &ae)
}

// Sync brings this device and its group in step once. Without a group it does nothing.
func (e *Engine) Sync(ctx context.Context) error {
	if e.relay == nil {
		return nil
	}
	e.run.Lock()
	defer e.run.Unlock()
	e.mu.Lock()
	e.lastAttempt = e.opts.Now()
	e.mu.Unlock()
	err := e.syncLocked(ctx)
	e.mu.Lock()
	e.lastError = err
	e.mu.Unlock()
	return err
}

func (e *Engine) syncLocked(ctx context.Context) error {
	for attempt := 0; attempt < 5; attempt++ {
		k, err := e.loadKeys(ctx)
		if err != nil || k == nil {
			return err
		}
		g := k.group()
		remote, version, err := e.fetch(ctx, g)
		if err != nil {
			return err
		}
		if remote.Moved != nil {
			if err := e.follow(remote.Moved, k); err != nil {
				return err
			}
			continue
		}
		st := e.loadState()
		if version == 0 && st.Version > 0 {
			// The group is gone from the relay: nothing to sync with any more.
			e.logf(slog.LevelWarn, "Sync group no longer exists, syncing is off")
			e.forget()
			return nil
		}
		e.mu.Lock()
		e.loadPendingLocked()
		p, gen := e.pending, e.pendingGen
		p.Changes = maps.Clone(p.Changes)
		e.mu.Unlock()
		// A sync.json from before keyed hashes is carried over by comparing values, secrets too.
		done, err := e.step(ctx, k, remote, version, st, p.Secrets || version == 0 || !st.Keyed, p)
		if done {
			e.donePending(gen)
		}
		if done || err != nil {
			return err
		}
	}
	return errConflict
}

// step is one round: stamp local changes, merge, apply what came in, write back. done is
// false when another device wrote in between and the round has to start over. p is what this
// device changed and when.
func (e *Engine) step(ctx context.Context, k *keys, remote Doc, version int64, st state, withSecrets bool, p pending) (bool, error) {
	snap, err := e.opts.Local.Snapshot(ctx, withSecrets)
	if err != nil {
		return false, err
	}
	me, now := k.Device, e.nowMS()
	sk := k.stateKey()
	h := func(v json.RawMessage) string { return keyedHash(sk, v) }
	if !st.Keyed {
		// sync.json of an older version: a value that still has the hash it was synced with is
		// unchanged here, any other one changed here. So an update sends nothing stale as new.
		for key, old := range st.Hashes {
			v, ok := snap[key]
			if !ok && strings.HasPrefix(key, DomainPrefix) {
				v, ok = jsonFalse, true
			}
			plain := old.H
			old.H = ""
			if ok && hash(v) == plain {
				old.H = h(v)
			}
			st.Hashes[key] = old
		}
	}
	// when: the time of this device's change of key, by which it competes with the others'.
	when := func(key, hv string) int64 {
		t := p.At
		if c, ok := p.Changes[key]; ok && (c.H == hv || c.H == "") {
			t = c.T
		}
		if t == 0 || t > now {
			t = now
		}
		return t
	}
	// changedHere: this device changed key to this value since its last round.
	changedHere := func(key, hv string) bool {
		c, ok := p.Changes[key]
		return ok && c.H == hv
	}
	snap[DevicePrefix+me] = mustJSON(Device{Name: e.opts.Name, Platform: e.opts.Platform,
		Pub: k.private().PublicKey().Bytes()})
	merged := maps.Clone(remote.Entries)
	// stamp offers a local value; the later change wins (docs/sync.md, Group document).
	stamp := func(key string, v json.RawMessage, at int64) {
		ent := Entry{V: compact(v), T: at, D: me}
		if r, ok := merged[key]; !ok || ent.newer(r) {
			merged[key] = ent
		}
	}
	own := DevicePrefix + me
	if r, ok := remote.Entries[own]; !ok || hash(r.V) != hash(snap[own]) {
		stamp(own, snap[own], now)
	}
	// Joining takes the group's settings. A device without its setup (config.toml missing or
	// unreadable) has none to offer: its empty snapshot would remove every website.
	if !st.Adopt && e.opts.Local.SetupDone() {
		for key, v := range snap {
			hv := h(v)
			old, known := st.Hashes[key]
			r, inRemote := remote.Entries[key]
			switch {
			case key == own:
			case known && old.H == hv: // unchanged here
			case inRemote && h(r.V) == hv: // already the same
			case !known && inRemote && !changedHere(key, hv): // never synced here: the group's value is applied below
			default:
				stamp(key, v, when(key, hv))
			}
		}
		removed := h(jsonFalse)
		for key, old := range st.Hashes {
			if _, present := snap[key]; present || old.H == removed || !strings.HasPrefix(key, DomainPrefix) {
				continue
			}
			stamp(key, jsonFalse, when(key, removed)) // removed here
		}
	}
	// What the group has that this device does not.
	local := func(key string) (string, bool) {
		if v, ok := snap[key]; ok {
			return h(v), true
		}
		if old, ok := st.Hashes[key]; ok && isSecret(key) && !withSecrets {
			return old.H, true
		}
		if strings.HasPrefix(key, DomainPrefix) {
			return h(jsonFalse), true
		}
		return "", false
	}
	var incoming []string
	for key, ent := range merged {
		if strings.HasPrefix(key, DevicePrefix) {
			continue
		}
		if lh, ok := local(key); !ok || lh != h(ent.V) {
			incoming = append(incoming, key)
		}
	}
	var failed map[string]error
	if len(incoming) > 0 {
		e.mu.Lock()
		e.applying = true
		e.mu.Unlock()
		failed = e.opts.Local.Apply(ctx, merged, incoming)
		e.mu.Lock()
		e.applying = false
		e.mu.Unlock()
		var applied []string
		for _, key := range incoming {
			if failed[key] == nil {
				applied = append(applied, key)
			}
		}
		if len(applied) > 0 {
			e.logf(slog.LevelInfo, "Synced from other devices: %s", strings.Join(sorted(applied), ", "))
		}
	}
	if !sameEntries(merged, remote.Entries) {
		next, err := e.store(ctx, k.group(), version, Doc{Format: Format, Entries: merged}, false)
		if errors.Is(err, errConflict) {
			// What Apply took over is the group's now, not a change made here: the next attempt
			// must not send it back with this round's time over a newer one from elsewhere.
			for _, key := range incoming {
				if failed[key] == nil {
					ent := merged[key]
					st.Hashes[key] = savedHash{H: h(ent.V), T: ent.T, D: ent.D}
				}
			}
			st.Keyed = true // the hashes were carried over above
			if err := e.saveState(st); err != nil {
				return false, err
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		version = next
	}
	saved := state{Version: version, Keyed: true, Hashes: map[string]savedHash{}}
	for key, ent := range merged {
		hv := h(ent.V)
		if failed[key] != nil {
			// Not taken over: what this device has counts as seen, so it is not sent back as a
			// change of its own; a newer value from the group is tried again.
			var ok bool
			if hv, ok = local(key); !ok {
				continue
			}
		}
		saved.Hashes[key] = savedHash{H: hv, T: ent.T, D: ent.D}
	}
	if err := e.saveState(saved); err != nil {
		return false, err
	}
	e.setDevices(merged, me)
	if len(failed) > 0 {
		ae := &ApplyError{}
		for _, key := range sorted(slices.Collect(maps.Keys(failed))) {
			ae.Keys = append(ae.Keys, key)
			if ae.Err == nil {
				ae.Err = failed[key]
			}
		}
		e.logf(slog.LevelWarn, "%s", ae)
		return true, ae
	}
	return true, nil
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func sameEntries(a, b map[string]Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x.T != y.T || x.D != y.D || hash(x.V) != hash(y.V) {
			return false
		}
	}
	return true
}

func (e *Engine) setDevices(entries map[string]Entry, me string) {
	var list []DeviceInfo
	for key, ent := range entries {
		if !strings.HasPrefix(key, DevicePrefix) || isNull(ent.V) {
			continue
		}
		var dv Device
		if json.Unmarshal(ent.V, &dv) != nil {
			continue
		}
		id := strings.TrimPrefix(key, DevicePrefix)
		list = append(list, DeviceInfo{ID: id, Name: dv.Name, Platform: dv.Platform, This: id == me})
	}
	// This device first, then by name.
	slices.SortFunc(list, func(a, b DeviceInfo) int {
		switch {
		case a.This != b.This:
			if a.This {
				return -1
			}
			return 1
		case a.Name != b.Name:
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.ID, b.ID)
	})
	e.mu.Lock()
	e.devices = list
	e.mu.Unlock()
}

// follow moves to the new group after a removal, or turns syncing off when this device was
// the one removed.
func (e *Engine) follow(m *Moved, k *keys) error {
	sealed, ok := m.Keys[k.Device]
	if !ok {
		e.logf(slog.LevelInfo, "This device was removed from syncing")
		e.forget()
		return nil
	}
	root, err := unbox(k.private(), sealed)
	if err != nil || len(root) != 32 {
		return errDecrypt
	}
	return e.saveKeys(newKeys(root, k.Device, k.Priv))
}

// Remove takes a device out of the group. The others move to a new key it never sees;
// removing this device turns syncing off here.
func (e *Engine) Remove(ctx context.Context, device string) error {
	if e.relay == nil {
		return ErrUnavailable
	}
	if err := e.Sync(ctx); err != nil && !partly(err) {
		return offline(err)
	}
	e.run.Lock()
	defer e.run.Unlock()
	for attempt := 0; attempt < 5; attempt++ {
		k, err := e.loadKeys(ctx)
		if err != nil || k == nil {
			return err
		}
		old := k.group()
		remote, version, err := e.fetch(ctx, old)
		if err != nil {
			return offline(err)
		}
		if remote.Moved != nil {
			if err := e.follow(remote.Moved, k); err != nil {
				return err
			}
			continue
		}
		if version == 0 {
			return nil
		}
		next := newGroup()
		sealed := map[string][]byte{}
		for key, ent := range remote.Entries {
			id := strings.TrimPrefix(key, DevicePrefix)
			if !strings.HasPrefix(key, DevicePrefix) || isNull(ent.V) || id == device {
				continue
			}
			var dv Device
			if json.Unmarshal(ent.V, &dv) != nil {
				continue
			}
			if sealed[id], err = box(dv.Pub, next.root); err != nil {
				return err
			}
		}
		entries := maps.Clone(remote.Entries)
		gone := Entry{V: json.RawMessage("null"), T: e.nowMS(), D: k.Device}
		if r, ok := entries[DevicePrefix+device]; ok && r.T >= gone.T {
			gone.T = r.T + 1
		}
		entries[DevicePrefix+device] = gone
		if _, err := e.store(ctx, next, 0, Doc{Format: Format, Entries: entries}, false); err != nil {
			return offline(err)
		}
		if _, err := e.store(ctx, old, version, Doc{Format: Format, Moved: &Moved{Keys: sealed}}, true); errors.Is(err, errConflict) {
			continue
		} else if err != nil {
			return offline(err)
		}
		if device == k.Device {
			e.logf(slog.LevelInfo, "Syncing turned off on this device")
			e.forget()
			return nil
		}
		e.logf(slog.LevelInfo, "Device %s removed from syncing", device)
		if err := e.saveKeys(newKeys(next.root, k.Device, k.Priv)); err != nil {
			return err
		}
		st := e.loadState()
		st.Version = 0 // the new group starts over; the hashes stay valid
		if err := e.saveState(st); err != nil {
			return err
		}
		e.setDevices(entries, k.Device)
		go e.Wake()
		return nil
	}
	return errConflict
}

// --- Status -------------------------------------------------------------------------

// Status is what the app shows.
type Status struct {
	Available bool
	On        bool
	Devices   []DeviceInfo
	Pairing   *Pairing
	Error     error // the last sync's error, nil after a good one
}

// Status reports without touching the network or the keyring.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Available: e.relay != nil, On: e.keys != nil, Devices: append([]DeviceInfo(nil), e.devices...),
		Error: e.lastError}
	if e.pairing != nil {
		p := *e.pairing
		s.Pairing = &p
	}
	return s
}

// Load reads the keys once so Status knows whether syncing is on.
func (e *Engine) Load(ctx context.Context) {
	if e.relay == nil {
		return
	}
	if _, err := e.loadKeys(ctx); err != nil {
		e.logf(slog.LevelWarn, "Could not read the sync keys: %s", err)
	}
}
