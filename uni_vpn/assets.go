// Package assets carries the files the Python package and the Go core share, so neither keeps
// its own copy: the university registry and the app page.
package assets

import _ "embed"

//go:embed universities.json
var Universities []byte

//go:embed ui/index.html
var IndexHTML []byte
