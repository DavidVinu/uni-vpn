// Package i18n mirrors uni_vpn/i18n.py: one flat JSON catalog per language in
// uni_vpn/locales/, English (en.json) is the source.
//
// The service keeps speaking English (log, command line). Texts the app shows are Text values:
// they render in English and also carry their catalog key and arguments, so status.json and the
// API can hand both to the app, which shows them in the user's language.
package i18n

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	assets "github.com/DavidVinu/uni-vpn/uni_vpn"
)

// Language is a code and the language's name in itself.
type Language struct{ Code, Name string }

// Languages: every language spoken at a university in universities.json; Chinese in both
// scripts (Hong Kong writes Traditional, most students from the mainland read Simplified).
var Languages = []Language{
	{"en", "English"},
	{"de", "Deutsch"},
	{"fr", "Français"},
	{"es", "Español"},
	{"zh-Hans", "简体中文"},
	{"zh-Hant", "繁體中文"},
}

// Source is the language the texts are written in.
const Source = "en"

type catalogFile struct {
	ordered pyjson.Object // file order, for /locales.json
	texts   map[string]string
}

var catalogs = map[string]catalogFile{}

func init() {
	for _, lang := range Languages {
		data, err := assets.Locales.ReadFile("locales/" + lang.Code + ".json")
		if err != nil {
			panic(err)
		}
		parsed, err := pyjson.Unmarshal(data)
		if err != nil {
			panic(fmt.Sprintf("locales/%s.json: %v", lang.Code, err))
		}
		obj := parsed.(pyjson.Object)
		texts := make(map[string]string, len(obj))
		for _, m := range obj {
			texts[m.Key] = m.Value.(string)
		}
		catalogs[lang.Code] = catalogFile{obj, texts}
	}
}

// Catalog is the texts of one language by key.
func Catalog(code string) map[string]string { return catalogs[code].texts }

// Catalogs is everything the app needs to switch languages: the choices and all catalogs.
func Catalogs() pyjson.Object {
	languages := make([]any, len(Languages))
	all := make(pyjson.Object, len(Languages))
	for i, lang := range Languages {
		languages[i] = []any{lang.Code, lang.Name}
		all[i] = pyjson.Member{Key: lang.Code, Value: catalogs[lang.Code].ordered}
	}
	return pyjson.O("languages", languages, "catalogs", all)
}

// Valid: a language setting is "" (follow the system) or one of Languages.
func Valid(code string) bool { return code == "" || known(code) }

func known(code string) bool {
	_, ok := catalogs[code]
	return ok
}

// Codes lists the language codes, comma separated.
func Codes() string {
	codes := make([]string, len(Languages))
	for i, lang := range Languages {
		codes[i] = lang.Code
	}
	return strings.Join(codes, ", ")
}

// Arg is one named argument of a Text. Value is a string, a number, a Text or a []any of those.
type Arg struct {
	Name  string
	Value any
}

// Text is English text that remembers its catalog key and arguments. It is an error too, so
// it can be returned where Python raises ValueError(t(...)).
type Text struct {
	Key  string
	Args []Arg
}

// T is the text with this key; args are name, value pairs.
func T(key string, args ...any) Text {
	text := Text{Key: key}
	for i := 0; i+1 < len(args); i += 2 {
		text.Args = append(text.Args, Arg{args[i].(string), args[i+1]})
	}
	return text
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// String is the English text.
func (t Text) String() string {
	return placeholder.ReplaceAllStringFunc(Catalog(Source)[t.Key], func(match string) string {
		name := match[1 : len(match)-1]
		for _, arg := range t.Args {
			if arg.Name == name {
				return plain(arg.Value)
			}
		}
		return match
	})
}

func (t Text) Error() string { return t.String() }

// plain is an argument in English: lists (several errors) one per line.
func plain(value any) string {
	if list, ok := value.([]any); ok {
		lines := make([]string, len(list))
		for i, item := range list {
			lines[i] = plain(item)
		}
		return strings.Join(lines, "\n")
	}
	return fmt.Sprint(value)
}

// JSON is the text for status.json and the API: {"key", "args"}.
func (t Text) JSON() pyjson.Object {
	args := pyjson.Object{}
	for _, arg := range t.Args {
		args = append(args, pyjson.Member{Key: arg.Name, Value: argJSON(arg.Value)})
	}
	return pyjson.O("key", t.Key, "args", args)
}

func argJSON(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = argJSON(item)
		}
		return out
	case int, int64, float64:
		return v
	case Text:
		return v.JSON()
	}
	return fmt.Sprint(value)
}

// Of is the {"key", "args"} of an error raised with a Text (also wrapped), else nil.
func Of(err error) any {
	var text Text
	if err != nil && errors.As(err, &text) {
		return text.JSON()
	}
	return nil
}

// Negotiate is the language to use: the setting, else the first system language there is a
// catalog for ("de-DE,en"), else English. Same rule as the page's systemLanguage().
func Negotiate(setting, accepted string) string {
	if known(setting) {
		return setting
	}
	for _, tag := range strings.Split(strings.ReplaceAll(accepted, "_", "-"), ",") {
		tag, _, _ = strings.Cut(tag, ";")
		parts := strings.Split(strings.ToLower(strings.TrimSpace(tag)), "-")
		base, rest := parts[0], parts[1:]
		if base == "zh" {
			hant := false
			for _, part := range rest {
				switch part {
				case "hans":
					return "zh-Hans"
				case "hant", "tw", "hk", "mo":
					hant = true
				}
			}
			if hant {
				return "zh-Hant"
			}
			return "zh-Hans"
		}
		if known(base) {
			return base
		}
	}
	return Source
}

// Menu is the texts for the menu bar and tray menus of the native app, which have no page to
// translate them.
func Menu(code string) pyjson.Object {
	own := Catalog(code)
	menu := pyjson.Object{}
	for _, m := range catalogs[Source].ordered {
		if strings.HasPrefix(m.Key, "menu.") || strings.HasPrefix(m.Key, "look.") {
			text, ok := own[m.Key]
			if !ok {
				text = m.Value.(string)
			}
			menu = append(menu, pyjson.Member{Key: m.Key, Value: text})
		}
	}
	return menu
}
