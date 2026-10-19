//go:build !windows

package daemon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
)

// setUpSyncDevice starts a device set up for Heidelberg (by hand, or by a pairing already).
func setUpSyncDevice(t *testing.T, url string) (*harness, *Daemon) {
	t.Helper()
	h := newHarness(t)
	h.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	return h, h.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path }))
}

func newSyncDevice(t *testing.T, url string) (*harness, *Daemon) {
	t.Helper()
	h := newHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	return h, h.start(syncOptions(url, dir, func(o *Options) { o.NeedsSetup, o.ConfigPath = true, path }))
}

// listOf: the websites a device uses (its saved list, else its defaults).
func listOf(t *testing.T, d *Daemon) []string {
	t.Helper()
	return slices.Clone(statusValue(t, d, "domains").([]string))
}

func setWebsite(t *testing.T, d *Daemon, host string, on bool) {
	t.Helper()
	list := slices.DeleteFunc(listOf(t, d), func(s string) bool { return s == host })
	if on {
		list = append(list, host)
	}
	if _, err := d.SetDomains(strings.Join(list, "\n")); err != nil {
		t.Fatal(err)
	}
}

// A website removed before syncing was turned on is in no group entry. A device set up by hand
// that enters this device's code takes all its settings ("joins"), so it drops the website
// too and does not send it back.
func TestSetUpDeviceJoiningDropsWebsitesTheGroupDoesNotList(t *testing.T) {
	_, url := relaytest.Start(t)
	_, laptop := setUpSyncDevice(t, url)
	setWebsite(t, laptop, "sogo.uni-heidelberg.de", false)
	_, phone := setUpSyncDevice(t, url) // the default websites
	pairWith(t, laptop, phone)
	if got := listOf(t, phone); slices.Contains(got, "sogo.uni-heidelberg.de") {
		t.Fatal("the phone kept it:", got)
	}
	syncRound(t, phone, laptop, phone, laptop)
	if got := listOf(t, laptop); slices.Contains(got, "sogo.uni-heidelberg.de") {
		t.Fatal("back on the laptop:", got)
	}
	if got := listOf(t, phone); !slices.Contains(got, "moodle.uni-heidelberg.de") {
		t.Fatal("the phone lost the group's websites:", got)
	}
}

// Every website removed and synced: a new device joins with the empty list, not with its
// defaults, and does not send the defaults back.
func TestJoiningAGroupWithoutWebsites(t *testing.T) {
	_, url := relaytest.Start(t)
	_, laptop := setUpSyncDevice(t, url)
	if _, err := laptop.SetDomains(""); err != nil {
		t.Fatal(err)
	}
	_, phone := newSyncDevice(t, url)
	pairWith(t, laptop, phone)
	if got := listOf(t, phone); len(got) != 0 {
		t.Fatal("the phone joined with", got)
	}
	syncRound(t, phone, laptop, phone, laptop)
	if got := listOf(t, laptop); len(got) != 0 {
		t.Fatal("back on the laptop:", got)
	}
}
