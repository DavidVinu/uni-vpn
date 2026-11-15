package devsync

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A change made on a device after it took over another device's value comes after that value,
// also when the other device's clock is ahead: the phone's language, website removal and
// password, changed after it synced the laptop's, must not be put back to the laptop's.
func TestChangeAfterTakingOverAValueFromAClockAheadStays(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	lc := &clock{t: time.Unix(1_800_000_000, 0).Add(2 * time.Hour)} // the laptop's clock, 2 h ahead
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), lc)
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	pair(t, ctx, laptop, phone)

	laptop.local.set(KeyLanguage, "de")
	laptop.local.setDomains("sogo.uni-heidelberg.de", "heico.uni-heidelberg.de", "example.org")
	laptop.local.set(KeyPassword, "from-the-laptop")
	laptop.Notify(KeyPassword)
	for _, d := range []device{laptop, phone} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if phone.local.get(KeyLanguage) != `"de"` || !slices.Contains(phone.local.getDomains(), "example.org") {
		t.Fatal("the phone did not take the laptop's settings")
	}
	// Ten minutes later the user changes all three on the phone.
	c.mu.Lock()
	c.t = c.t.Add(10 * time.Minute)
	c.mu.Unlock()
	phone.local.set(KeyLanguage, "fr")
	phone.local.setDomains("sogo.uni-heidelberg.de", "heico.uni-heidelberg.de")
	phone.local.set(KeyPassword, "typed-on-the-phone")
	phone.Notify(KeyPassword)
	for _, d := range []device{phone, laptop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []device{phone, laptop} {
		if got := d.local.get(KeyLanguage); got != `"fr"` {
			t.Errorf("%s: language %s, want the phone's later fr", d.opts.Name, got)
		}
		if got := d.local.getDomains(); slices.Contains(got, "example.org") {
			t.Errorf("%s: the website removed on the phone came back: %v", d.opts.Name, got)
		}
		if got := d.local.get(KeyPassword); got != `"typed-on-the-phone"` {
			t.Errorf("%s: password %s, want the one typed on the phone", d.opts.Name, got)
		}
	}
}
