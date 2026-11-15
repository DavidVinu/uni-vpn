package i18n

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

func TestNegotiateLikeThePage(t *testing.T) {
	for _, c := range []struct{ setting, accepted, want string }{
		{"", "de-DE,en", "de"},
		{"", "ja-JP,fr-CA", "fr"},
		{"", "zh-Hant-HK", "zh-Hant"},
		{"", "zh_HK", "zh-Hant"},
		{"", "zh-Hans-HK", "zh-Hans"},
		{"", "zh-CN", "zh-Hans"},
		{"", "ja", "en"},
		{"", "", "en"},
		{"", "en;q=0.8", "en"},
		{"es", "de", "es"},
		{"xx", "de", "de"},
	} {
		if got := Negotiate(c.setting, c.accepted); got != c.want {
			t.Errorf("Negotiate(%q, %q) = %q, want %q", c.setting, c.accepted, got, c.want)
		}
	}
}

func TestMenuHasEveryStateAndItem(t *testing.T) {
	menu := Menu("de")
	get := func(key string) any { v, _ := menu.Get(key); return v }
	if get("menu.connect") != "Verbinden" || get("look.idle") != "Nicht verbunden" || get("settings.title") != nil {
		t.Fatal(menu)
	}
	if len(menu) != len(Menu("en")) {
		t.Fatal(len(menu))
	}
}

func TestTextIsEnglishAndCarriesItsKey(t *testing.T) {
	text := T("domains.not_hostname", "line", 3, "text", "x y")
	if text.String() != `Line 3: "x y" is not a website` {
		t.Fatal(text)
	}
	if got := string(pyjson.Marshal(text.JSON())); got != `{"key": "domains.not_hostname", "args": {"line": 3, "text": "x y"}}` {
		t.Fatal(got)
	}
	lines := T("app.lines", "lines", []any{T("domains.not_hostname", "line", 2, "text", "a b"), "plain"})
	if lines.String() != "Line 2: \"a b\" is not a website\nplain" {
		t.Fatal(lines)
	}
	if got := string(pyjson.Marshal(lines.JSON())); got != `{"key": "app.lines", "args": {"lines": `+
		`[{"key": "domains.not_hostname", "args": {"line": 2, "text": "a b"}}, "plain"]}}` {
		t.Fatal(got)
	}
	wrapped := fmt.Errorf("saving: %w", T("setup.finish_first"))
	if Of(wrapped) == nil || Of(errors.New("x")) != nil || Of(nil) != nil {
		t.Fatal(Of(wrapped))
	}
}

func TestValid(t *testing.T) {
	if !Valid("") || !Valid("zh-Hant") || Valid("zh-hant") || Valid("xx") {
		t.Fatal()
	}
}

// The languages and the rule must match uni_vpn/i18n.py.
func TestSameAsPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	cmd := exec.Command(python, "-c", `import json
from uni_vpn import i18n
tags = ["de-DE,en", "zh-TW", "zh-hans-tw", "pt-BR, es;q=0.5", "  FR ", "", "zh"]
print(json.dumps({"catalogs": i18n.catalogs(), "negotiate": {t: i18n.negotiate("", t) for t in tags},
                  "menu": i18n.menu("zh-Hant")}))`)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skip("python core not importable:", err)
	}
	var want struct {
		Catalogs  any               `json:"catalogs"`
		Negotiate map[string]string `json:"negotiate"`
		Menu      map[string]string `json:"menu"`
	}
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	var got any
	json.Unmarshal(pyjson.Marshal(Catalogs()), &got)
	if fmt.Sprint(got) != fmt.Sprint(want.Catalogs) {
		t.Error("catalogs differ")
	}
	for tag, lang := range want.Negotiate {
		if Negotiate("", tag) != lang {
			t.Errorf("Negotiate(%q) = %q, Python %q", tag, Negotiate("", tag), lang)
		}
	}
	var menu map[string]string
	json.Unmarshal(pyjson.Marshal(Menu("zh-Hant")), &menu)
	if fmt.Sprint(menu) != fmt.Sprint(want.Menu) {
		t.Error("menus differ")
	}
}

// The phone apps show phone.msg.port_in_use when the core cannot start, so they carry their own
// copy of it (Android's port_in_use, iOS's Strings.portTaken). A change to the catalog must reach
// both; the iOS test checks its copy only on a Mac.
func TestPhoneAppsCarryTheCoresPortText(t *testing.T) {
	root := filepath.Join("..", "..")
	swift, err := os.ReadFile(filepath.Join(root, "app", "ios", "Shared", "Strings.swift"))
	if err != nil {
		t.Fatal(err)
	}
	android := map[string]string{"en": "values", "de": "values-de", "fr": "values-fr", "es": "values-es",
		"zh-Hans": "values-zh-rCN", "zh-Hant": "values-zh-rTW"}
	for code, dir := range android {
		want := Catalog(code)["phone.msg.port_in_use"]
		data, err := os.ReadFile(filepath.Join(root, "app", "android", "app", "src", "main", "res", dir, "strings.xml"))
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Strings []struct {
				Name string `xml:"name,attr"`
				Text string `xml:",chardata"`
			} `xml:"string"`
		}
		if err := xml.Unmarshal(data, &res); err != nil {
			t.Fatal(dir, err)
		}
		got := ""
		for _, s := range res.Strings {
			if s.Name == "port_in_use" {
				got = strings.NewReplacer(`\'`, `'`, `\"`, `"`).Replace(s.Text)
			}
		}
		if got != want {
			t.Errorf("Android %s port_in_use = %q, catalog %s %q", dir, got, code, want)
		}
		got = ""
		if m := regexp.MustCompile(`(?m)^\s*"` + regexp.QuoteMeta(code) + `": "([^"]*)",`).FindSubmatch(swift); m != nil {
			got = string(m[1])
		}
		if got != want {
			t.Errorf("iOS Strings.portTaken %s = %q, catalog %q", code, got, want)
		}
	}
}
