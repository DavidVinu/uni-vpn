package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/devsync"
	"github.com/DavidVinu/uni-vpn/internal/httpapi"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/pac"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// SyncRelay is the relay this build syncs through (relay/, docs/sync.md); "" hides
// syncing. UNI_VPN_SYNC_RELAY replaces it, for a relay of one's own or for tests.
var SyncRelay = "https://uni-vpn-sync.david-noel-vinu.workers.dev"

func syncRelay() string {
	if v := os.Getenv("UNI_VPN_SYNC_RELAY"); v != "" {
		return v
	}
	return SyncRelay
}

// keyringSync is the sync keys' keyring entry.
type keyringSync struct{ d *Daemon }

func (k keyringSync) Load(ctx context.Context) (string, error) {
	_, timeout := k.d.keyringArgs()
	value, err := credentials.GetSecret(ctx, credentials.SyncUser, credentials.KindSync, timeout, nil)
	if errors.Is(err, credentials.ErrSecretMissing) {
		return "", nil
	}
	return string(value), err
}

func (keyringSync) Save(value string) error {
	return credentials.StoreSecret(credentials.SyncUser, credentials.KindSync, value)
}

func (keyringSync) Delete() error {
	_, err := credentials.DeleteSecret(credentials.SyncUser, credentials.KindSync)
	return err
}

func deviceName() string {
	// Android's host name is "localhost"; the app passes the phone's name.
	if name := os.Getenv("UNI_VPN_DEVICE_NAME"); name != "" {
		return name
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return platformName()
	}
	return strings.TrimSuffix(name, ".local")
}

func (d *Daemon) newSync() *devsync.Engine {
	o := d.opts
	if o.SyncStore == nil {
		o.SyncStore = keyringSync{d}
	}
	if o.SyncStatePath == "" {
		o.SyncStatePath = filepath.Join(platform.ConfigDir(), "sync.json")
	}
	return devsync.New(devsync.Options{Relay: o.SyncRelay, StatePath: o.SyncStatePath, Store: o.SyncStore,
		Local: syncLocal{d}, Name: deviceName(), Platform: platformName(), Log: d.log})
}

// syncChanged: a setting changed here; secrets when password or second factor did.
func (d *Daemon) syncChanged(secrets bool) { d.sync.Notify(secrets) }

// syncLocal is this device's side of syncing: config.toml, the website list and the keyring.
type syncLocal struct{ d *Daemon }

func (l syncLocal) SetupDone() bool {
	l.d.mu.Lock()
	defer l.d.mu.Unlock()
	return !l.d.needsSetup && l.d.configError == ""
}

// syncAccount is the "account" value: everything the setup assistant asks except secrets.
type syncAccount struct {
	University string   `json:"university"`
	User       string   `json:"user"`
	Overrides  [][2]any `json:"overrides"` // [field, value] in config.toml's order
}

func (l syncLocal) Snapshot(ctx context.Context, withSecrets bool) (map[string]json.RawMessage, error) {
	d := l.d
	out := map[string]json.RawMessage{}
	if !l.SetupDone() {
		return out, nil
	}
	d.mu.Lock()
	cfg := *d.cfg
	path := d.configPath
	d.mu.Unlock()
	overrides, err := config.Overrides(path)
	if err != nil {
		return nil, err
	}
	account := syncAccount{University: cfg.University, User: cfg.User, Overrides: [][2]any{}}
	for _, s := range overrides {
		account.Overrides = append(account.Overrides, [2]any{s.Key, s.Value})
	}
	put := func(key string, v any) {
		b, _ := json.Marshal(v)
		out[key] = b
	}
	put(devsync.KeyAccount, account)
	put(devsync.KeyLanguage, cfg.Language)
	put(devsync.KeyAutoUpdate, cfg.AutoUpdate)
	domains, err := pac.ReadDomains(d.opts.DomainsPath, cfg.DefaultDomains)
	if err != nil {
		return nil, err
	}
	for _, host := range domains {
		out[devsync.DomainPrefix+host] = json.RawMessage("true")
	}
	if withSecrets {
		password, err := d.opts.PasswordGetter(ctx)
		if err != nil {
			return nil, err
		}
		put(devsync.KeyPassword, string(password))
		token := ""
		if cfg.NeedsTOTP() {
			value, err := d.opts.TOTPGetter(ctx)
			if err != nil {
				return nil, err
			}
			token = string(value)
		}
		put(devsync.KeyTOTP, token)
	}
	return out, nil
}

func (l syncLocal) Apply(ctx context.Context, all map[string]devsync.Entry, keys []string) map[string]error {
	d := l.d
	failed := map[string]error{}
	fail := func(err error, keys ...string) {
		for _, key := range keys {
			failed[key] = err
		}
	}
	changed := func(key string) bool { return slices.Contains(keys, key) }
	text := func(key string) string {
		var s string
		if e, ok := all[key]; ok {
			json.Unmarshal(e.V, &s)
		}
		return s
	}
	secrets := []string{devsync.KeyAccount, devsync.KeyPassword, devsync.KeyTOTP}
	if changed(devsync.KeyAccount) || !l.SetupDone() {
		if err := l.applyAccount(all, text(devsync.KeyPassword), text(devsync.KeyTOTP)); err != nil {
			fail(err, secrets...)
		}
	} else {
		if changed(devsync.KeyPassword) && text(devsync.KeyPassword) != "" {
			if err := d.SetPassword(text(devsync.KeyPassword)); err != nil {
				fail(err, devsync.KeyPassword)
			}
		}
		if changed(devsync.KeyTOTP) && text(devsync.KeyTOTP) != "" {
			if err := d.SetTOTP(text(devsync.KeyTOTP)); err != nil {
				fail(err, devsync.KeyTOTP)
			}
		}
	}
	if changed(devsync.KeyLanguage) {
		if err := d.SetLanguage(text(devsync.KeyLanguage)); err != nil {
			fail(err, devsync.KeyLanguage)
		}
	}
	if changed(devsync.KeyAutoUpdate) {
		var on bool
		err := json.Unmarshal(all[devsync.KeyAutoUpdate].V, &on)
		if err == nil {
			err = d.SetAutoUpdate(on)
		}
		if err != nil {
			fail(err, devsync.KeyAutoUpdate)
		}
	}
	var domainKeys []string
	for _, key := range keys {
		if strings.HasPrefix(key, devsync.DomainPrefix) {
			domainKeys = append(domainKeys, key)
		}
	}
	if len(domainKeys) > 0 {
		if err := l.applyDomains(all); err != nil {
			fail(err, domainKeys...)
		}
	}
	return failed
}

// applyAccount is the setup assistant with the group's answers.
func (l syncLocal) applyAccount(all map[string]devsync.Entry, password, token string) error {
	d := l.d
	var account syncAccount
	if e, ok := all[devsync.KeyAccount]; !ok || json.Unmarshal(e.V, &account) != nil {
		return errors.New("the synced settings have no account")
	}
	if password == "" {
		return errors.New("the synced settings have no password")
	}
	var overrides []config.Setting
	for _, o := range account.Overrides {
		if key, ok := o[0].(string); ok {
			overrides = append(overrides, config.Setting{Key: key, Value: o[1]})
		}
	}
	// Overrides another device dropped go here too; the assistant only replaces values.
	d.mu.Lock()
	path, setUp := d.configPath, !d.needsSetup && d.configError == ""
	d.mu.Unlock()
	if setUp && exists(path) {
		current, err := config.Overrides(path)
		if err != nil {
			return err
		}
		var stale []string
		for _, s := range current {
			if !slices.ContainsFunc(overrides, func(o config.Setting) bool { return o.Key == s.Key }) {
				stale = append(stale, s.Key)
			}
		}
		if len(stale) > 0 {
			if err := config.RemoveKeys(path, stale); err != nil {
				return err
			}
		}
	}
	return d.CompleteSetup(account.User, password, token, account.University, overrides)
}

func (l syncLocal) applyDomains(all map[string]devsync.Entry) error {
	d := l.d
	d.mu.Lock()
	defaults := slices.Clone(d.cfg.DefaultDomains)
	d.mu.Unlock()
	current, err := pac.ReadDomains(d.opts.DomainsPath, defaults)
	if err != nil {
		return err
	}
	wanted := devsync.Domains(all)
	// The order on this device stays, new websites go at the end.
	var list []string
	for _, host := range current {
		if slices.Contains(wanted, host) {
			list = append(list, host)
		}
	}
	for _, host := range wanted {
		if !slices.Contains(list, host) {
			list = append(list, host)
		}
	}
	_, err = d.SetDomains(strings.Join(list, "\n"))
	return err
}

// --- The app's side -----------------------------------------------------------------------

// syncError is the text the app shows for a sync problem.
func syncError(err error) error {
	switch {
	case errors.Is(err, devsync.ErrCodeUnknown):
		return httpapi.TextValueError(i18n.T("sync.code_unknown"))
	case errors.Is(err, devsync.ErrSetupFirst):
		return httpapi.TextValueError(i18n.T("sync.setup_first"))
	case errors.Is(err, devsync.ErrUnavailable), errors.Is(err, devsync.ErrOffline):
		return httpapi.TextValueError(i18n.T("sync.offline"))
	}
	return err
}

func pairingJSON(p *devsync.Pairing) any {
	if p == nil {
		return nil
	}
	return pyjson.O("mode", p.Mode, "code", p.Code, "qr", p.QR, "state", p.State)
}

// syncStatus is the "sync" object of /status.json.
func (d *Daemon) syncStatus() pyjson.Object {
	s := d.sync.Status()
	devices := []any{}
	for _, dv := range s.Devices {
		devices = append(devices, pyjson.O("id", dv.ID, "name", dv.Name, "platform", dv.Platform, "this", dv.This))
	}
	return pyjson.O("available", s.Available, "on", s.On, "devices", devices, "pairing", pairingJSON(s.Pairing),
		"error", s.Error != nil)
}

// Sync is the app's /api/sync/<action>: offer, request, enter (code), cancel, remove
// (device), refresh.
func (d *Daemon) Sync(action string, data pyjson.Object) (pyjson.Object, error) {
	ctx := context.Background()
	e := d.sync
	switch action {
	case "offer", "request":
		var p devsync.Pairing
		var err error
		if action == "offer" {
			p, err = e.Offer(ctx)
		} else {
			p, err = e.Request(ctx)
		}
		if err != nil {
			return nil, syncError(err)
		}
		return pyjson.O("pairing", pairingJSON(&p)), nil
	case "enter":
		value, _ := data.Get("code")
		code, _ := value.(string)
		if err := e.Enter(ctx, code); err != nil {
			return nil, syncError(err)
		}
	case "cancel":
		e.CancelPairing()
	case "remove":
		value, _ := data.Get("device")
		id, _ := value.(string)
		if err := e.Remove(ctx, id); err != nil {
			return nil, syncError(err)
		}
	case "refresh":
		if err := e.Refresh(ctx); err != nil {
			var ae *devsync.ApplyError
			if errors.As(err, &ae) {
				return nil, err
			}
			return nil, syncError(fmt.Errorf("%w: %w", devsync.ErrOffline, err))
		}
	default:
		return nil, httpapi.ErrNotFound
	}
	return pyjson.O(), nil
}
