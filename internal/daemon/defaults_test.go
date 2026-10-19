//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/pac"
)

// firstStart: the first start of this version (after an update from an older one).
func firstStart(d *Daemon) {
	os.Remove(filepath.Join(d.opts.TokenDir, addedDefaultsName))
	os.Remove(filepath.Join(d.opts.TokenDir, pacSumName))
}

func websites(t *testing.T, h *harness) []string {
	t.Helper()
	list, err := pac.ReadDomains(h.domainsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func (h *harness) restart(before ...func(*Daemon)) *Daemon {
	h.teardown()
	h.d = nil
	h.mu.Lock()
	h.refreshed = nil
	h.mu.Unlock()
	return h.start(nil, before...)
}

func (h *harness) waitRefreshed(want []int) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		got := slices.Clone(h.refreshed)
		h.mu.Unlock()
		if slices.Equal(got, want) || time.Now().After(deadline) {
			// A refresh too many would come right after the first.
			time.Sleep(200 * time.Millisecond)
			h.mu.Lock()
			got = slices.Clone(h.refreshed)
			h.mu.Unlock()
			if !slices.Equal(got, want) {
				h.t.Fatalf("browsers told %v, want %v", got, want)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A list saved before Moodle, YoKI and the license portal were Heidelberg's websites gets them
// after the update, once: removed by the user, they stay removed.
func TestSavedListGetsANewDefaultWebsiteOnce(t *testing.T) {
	h := newHarness(t)
	if err := pac.WriteDomains(h.domainsPath, []string{"sogo.uni-heidelberg.de", "example.org"}); err != nil {
		t.Fatal(err)
	}
	d := h.start(nil, firstStart)
	want := []string{"sogo.uni-heidelberg.de", "example.org", "moodle.uni-heidelberg.de", "yoki.urz.uni-heidelberg.de",
		"m365.urz.uni-heidelberg.de", "lsf.uni-heidelberg.de", "telefonbuch.uni-heidelberg.de", "heibot-it.urz.uni-heidelberg.de"}
	if got := websites(t, h); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	h.waitRefreshed([]int{h.cfg.HTTPPort}) // once, not again for the changed rule
	if _, err := d.SetDomains("sogo.uni-heidelberg.de\nexample.org\nyoki.urz.uni-heidelberg.de"); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if got := websites(t, h); !slices.Equal(got, []string{want[0], want[1], want[3]}) {
		t.Fatal(got)
	}
	h.waitRefreshed(nil)
}

// Without a saved list the defaults apply, Moodle with them: nothing is written.
func TestDefaultWebsitesStayDefaults(t *testing.T) {
	h := newHarness(t)
	h.start(nil, firstStart)
	if _, err := os.Stat(h.domainsPath); err == nil {
		t.Fatal("domains.txt written")
	}
	if text, _ := h.d.PAC(); !strings.Contains(text, `"moodle.uni-heidelberg.de"`) {
		t.Fatal(text)
	}
}

// A list that has the university's whole domain covers Moodle already.
func TestSavedListWithTheWholeDomainStays(t *testing.T) {
	h := newHarness(t)
	if err := pac.WriteDomains(h.domainsPath, []string{"uni-heidelberg.de"}); err != nil {
		t.Fatal(err)
	}
	h.start(nil, firstStart)
	if got := websites(t, h); !slices.Equal(got, []string{"uni-heidelberg.de"}) {
		t.Fatal(got)
	}
}

// Another of the user's devices decided about Moodle (removed it, or added it and the list here
// has it removed): the group's choice stays.
func TestNewDefaultWebsiteTheGroupKnowsIsNotAdded(t *testing.T) {
	for _, file := range []string{"sync.json", "sync.json.pending"} {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t)
			if err := pac.WriteDomains(h.domainsPath, []string{"sogo.uni-heidelberg.de"}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			state := `{"version":3,"keyed":true,"hashes":{"domain/moodle.uni-heidelberg.de":{"h":"x","t":1,"d":"phone"}}}`
			if file == "sync.json.pending" {
				state = `{"at":1,"changes":{"domain/moodle.uni-heidelberg.de":{"h":"x","t":1}}}`
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(state), 0o600); err != nil {
				t.Fatal(err)
			}
			h.start(syncOptions("http://127.0.0.1:9", dir, nil), firstStart)
			want := []string{"sogo.uni-heidelberg.de", "yoki.urz.uni-heidelberg.de", "m365.urz.uni-heidelberg.de",
				"lsf.uni-heidelberg.de", "telefonbuch.uni-heidelberg.de", "heibot-it.urz.uni-heidelberg.de"}
			if got := websites(t, h); !slices.Equal(got, want) {
				t.Fatal(got)
			}
		})
	}
}

// An update with new default websites: the browsers load the new rule.
func TestBrowsersToldAboutARuleChangedByAnUpdate(t *testing.T) {
	h := newHarness(t)
	h.cfg.DefaultDomains = []string{"sogo.uni-heidelberg.de"} // the older version's
	h.start(nil, firstStart)
	h.waitRefreshed([]int{h.cfg.HTTPPort}) // the first start: no rule noted yet
	h.cfg.DefaultDomains = []string{"sogo.uni-heidelberg.de", "moodle.uni-heidelberg.de"}
	h.restart()
	h.waitRefreshed([]int{h.cfg.HTTPPort})
	h.restart()
	h.waitRefreshed(nil) // the same rule
}

// An installer run again (maybe an older one) set the rule's plain address: the browsers have
// the rule of whichever version answered then.
func TestBrowsersToldAfterAnInstallerSetThePlainAddress(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	h.waitRefreshed(nil)
	h.unversioned.Store(true)
	h.restart()
	h.waitRefreshed([]int{h.cfg.HTTPPort})
}

// A new install (not set up yet, no list): a list the user saves later has these websites, or
// they removed them, so none comes back.
func TestNewInstallNeedsNoNewDefaultWebsites(t *testing.T) {
	h := newHarness(t)
	h.start(func(o *Options) { o.NeedsSetup = true }, firstStart)
	if data, _ := os.ReadFile(filepath.Join(h.tokenDir, addedDefaultsName)); !strings.Contains(string(data), "yoki.urz.uni-heidelberg.de") {
		t.Fatalf("%q", data)
	}
}

// A list on a device not set up yet: its university is not known, the list stays until then.
func TestListWithoutSetupWaitsForIt(t *testing.T) {
	h := newHarness(t)
	if err := pac.WriteDomains(h.domainsPath, []string{"sogo.uni-heidelberg.de"}); err != nil {
		t.Fatal(err)
	}
	h.start(func(o *Options) { o.NeedsSetup = true }, firstStart)
	if got := websites(t, h); !slices.Equal(got, []string{"sogo.uni-heidelberg.de"}) {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(h.tokenDir, addedDefaultsName)); err == nil {
		t.Fatal("marked done")
	}
}

// Heidelberg's portal for code generators opens only on the university network or over the
// VPN: once the device is set up, the rule sends it through Uni VPN, and the browsers are told.
// Not before the setup (the listed websites wait for it), and not in the user's website list.
func TestVPNOnlyPortalGoesThroughOnceSetUp(t *testing.T) {
	h := newHarness(t)
	h.startSetup()
	rule := func() string {
		r := h.http("GET", "/proxy.pac", nil, "")
		if r.status != 200 {
			t.Fatal(r.status)
		}
		return string(r.body)
	}
	if got := rule(); strings.Contains(got, "mfa.uni-heidelberg.de") {
		t.Fatal("listed before the setup:", got)
	}
	if status, data := h.postSetup(`{"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ", "university": "heidelberg"}`); status != 200 {
		t.Fatal(status, data)
	}
	// Directly when Uni VPN refuses (a code no longer accepted, Cisco connected): on campus.
	want := pac.BuildPACOrDirect(pac.DefaultDomains, []string{"mfa.uni-heidelberg.de"}, h.cfg.SocksPort)
	if got := rule(); got != want {
		t.Fatal(got)
	}
	h.waitRefreshed([]int{h.cfg.HTTPPort})
	if data := h.http("GET", "/status.json", nil, "").json(t); strings.Contains(fmt.Sprint(data["domains"]), "mfa.") {
		t.Fatal(data["domains"])
	}
}

// A saved list that already covers the portal: nothing added, and the rule did not change.
func TestVPNOnlyPortalCoveredByTheSavedList(t *testing.T) {
	h := newHarness(t)
	if err := pac.WriteDomains(h.domainsPath, []string{"uni-heidelberg.de"}); err != nil {
		t.Fatal(err)
	}
	h.startSetup()
	if status, data := h.postSetup(`{"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ", "university": "heidelberg"}`); status != 200 {
		t.Fatal(status, data)
	}
	if r := h.http("GET", "/proxy.pac", nil, ""); strings.Contains(string(r.body), "mfa.") {
		t.Fatal(string(r.body))
	}
	h.waitRefreshed(nil)
}
