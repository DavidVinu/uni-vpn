// Package assets carries the files the Python package and the Go core share, so neither keeps
// its own copy: the university registry, the app page and its translations.
package assets

import "embed"

//go:embed universities.json
var Universities []byte

//go:embed ui/index.html
var IndexHTML []byte

// Locales are the translations, one flat JSON catalog per language (locales/<code>.json).
//
//go:embed locales/*.json
var Locales embed.FS
