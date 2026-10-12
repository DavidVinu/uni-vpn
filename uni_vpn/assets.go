// Package assets carries the files the Python package and the Go core share, so neither keeps
// its own copy: the university registry, the app page and its translations.
package assets

import (
	"embed"
	"regexp"
)

//go:embed universities.json
var Universities []byte

//go:embed ui/index.html
var IndexHTML []byte

// Locales are the translations, one flat JSON catalog per language (locales/<code>.json).
//
//go:embed locales/*.json
var Locales embed.FS

// Guides are the pictures of the second-factor walkthrough (ui/guides/<name>.svg), drawn after
// the universities' MFA portals.
//
//go:embed ui/guides/*.svg
var Guides embed.FS

var guideName = regexp.MustCompile(`\A[a-z0-9][a-z0-9-]{0,62}\.svg\z`)

// Guide returns the picture with this file name; ok is false for an unknown or invalid name.
func Guide(name string) (data []byte, ok bool) {
	if !guideName.MatchString(name) {
		return nil, false
	}
	data, err := Guides.ReadFile("ui/guides/" + name)
	return data, err == nil
}

// HasGuide reports whether ui/guides has this picture.
func HasGuide(name string) bool {
	_, ok := Guide(name)
	return ok
}
