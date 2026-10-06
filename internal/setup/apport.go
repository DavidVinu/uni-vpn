package setup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ApportIgnorePath keeps Ubuntu's crash reporter quiet about openconnect.
func (s *Setup) ApportIgnorePath() string { return filepath.Join(s.Home(), ".apport-ignore.xml") }

var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	"\n", "&#10;", "\r", "&#13;", "\t", "&#09;")

// apportRoot is what ElementTree would find in the file: the root element's name, its text
// from start to end tag, and the programs of the <ignore> entries directly inside it.
type apportRoot struct {
	name     string
	text     string
	programs []string
}

func parseApport(data []byte) (*apportRoot, bool) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root := &apportRoot{}
	depth := 0
	var start int64
	for {
		offset := dec.InputOffset()
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return root, root.name != "" && depth == 0
		}
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if root.name != "" {
					return nil, false // a second root
				}
				root.name, start = t.Name.Local, offset
			} else if depth == 1 && t.Name.Local == "ignore" {
				for _, attr := range t.Attr {
					if attr.Name.Local == "program" {
						root.programs = append(root.programs, attr.Value)
					}
				}
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				root.text = string(data[start:dec.InputOffset()])
			}
		}
	}
}

// EnsureApportIgnore adds an entry like apport's mark_ignore(): <ignore program=... mtime=...>.
// Returns the path if the file was newly created.
func (s *Setup) EnsureApportIgnore(executable string, dry bool) (string, error) {
	path := s.ApportIgnorePath()
	_, statErr := os.Stat(path)
	created := statErr != nil
	if dry {
		s.say("would add %s to %s", executable, path)
		return "", nil
	}
	root := &apportRoot{name: "apport", text: "<apport />"}
	if !created {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if parsed, ok := parseApport(data); ok {
			root = parsed
		}
	}
	for _, program := range root.programs {
		if program == executable {
			if created {
				return path, nil
			}
			return "", nil
		}
	}
	mtime := "0"
	if st, err := os.Stat(executable); err == nil {
		mtime = strconv.FormatInt(st.ModTime().Unix(), 10)
	}
	entry := `<ignore program="` + attrEscaper.Replace(executable) + `" mtime="` + mtime + `" />`
	text := root.text
	if end := strings.LastIndex(text, "</"); end >= 0 && strings.HasSuffix(text, ">") {
		text = text[:end] + entry + text[end:]
	} else {
		// A root without content: <apport/> or <apport />.
		text = "<" + root.name + ">" + entry + "</" + root.name + ">"
	}
	if err := os.WriteFile(path, []byte("<?xml version=\"1.0\"?>\n"+text+"\n"), 0o666); err != nil {
		return "", err
	}
	if created {
		return path, nil
	}
	return "", nil
}
