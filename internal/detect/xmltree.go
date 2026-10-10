package detect

// A minimal ElementTree: enough of xml.etree to read the AnyConnect auth form the way
// uni_vpn/detect.py does, with the strictness of expat where encoding/xml is laxer.

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

type element struct {
	name     xml.Name
	attrs    []xml.Attr
	text     string // ElementTree .text: character data before the first child
	children []*element
}

// is reports a tag without namespace, as ElementTree compares "{ns}tag" against "tag".
func (e *element) is(tag string) bool { return e.name.Space == "" && e.name.Local == tag }

// get mirrors Element.get; ok is false where Python returns None.
func (e *element) get(key string) (string, bool) {
	for _, a := range e.attrs {
		if a.Name.Space == "" && a.Name.Local == key {
			return a.Value, true
		}
	}
	return "", false
}

// find mirrors Element.find(tag): the first direct child.
func (e *element) find(tag string) *element {
	for _, c := range e.children {
		if c.is(tag) {
			return c
		}
	}
	return nil
}

// findAll mirrors Element.findall(tag): direct children only.
func (e *element) findAll(tag string) []*element {
	var out []*element
	for _, c := range e.children {
		if c.is(tag) {
			out = append(out, c)
		}
	}
	return out
}

// iter mirrors Element.iter(tag): the element itself and all descendants, document order.
func (e *element) iter(tag string, fn func(*element) bool) bool {
	if e.is(tag) && fn(e) {
		return true
	}
	for _, c := range e.children {
		if c.iter(tag, fn) {
			return true
		}
	}
	return false
}

// findText mirrors Element.findtext(path) for a path of child tags: "" when nothing matches.
func (e *element) findText(path ...string) string {
	if len(path) == 0 {
		return e.text
	}
	for _, c := range e.findAll(path[0]) {
		if len(path) == 1 {
			return c.text
		}
		if c.find(path[1]) != nil {
			return c.findText(path[1:]...)
		}
	}
	return ""
}

var errXML = errors.New("not well-formed XML")

func isXMLSpace(s string) bool { return strings.Trim(s, " \t\r\n") == "" }

// charsetReader covers the encodings expat knows besides UTF-8 (UTF-16 is not supported).
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "iso-8859-1":
		return &latin1Reader{r: bufio.NewReader(input)}, nil
	case "us-ascii":
		return &latin1Reader{r: bufio.NewReader(input), ascii: true}, nil
	}
	return nil, fmt.Errorf("unsupported encoding %q", charset)
}

type latin1Reader struct {
	r     *bufio.Reader
	ascii bool
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	n := 0
	for n+2 <= len(p) {
		b, err := l.r.ReadByte()
		if err != nil {
			if n > 0 && err == io.EOF {
				return n, nil
			}
			return n, err
		}
		if b < 0x80 {
			p[n] = b
			n++
			continue
		}
		if l.ascii {
			return n, errXML
		}
		p[n], p[n+1] = 0xc0|b>>6, 0x80|b&0x3f
		n += 2
	}
	return n, nil
}

// parseXML mirrors ET.fromstring: one root element, nothing but whitespace, comments and
// processing instructions around it. Directives (DOCTYPE) are refused outright.
func parseXML(data []byte) (*element, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.CharsetReader = charsetReader
	var root *element
	var stack []*element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && root != nil {
				return nil, errXML // junk after document element
			}
			el := &element{name: t.Name, attrs: t.Attr}
			if len(stack) == 0 {
				root = el
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, el)
			}
			stack = append(stack, el)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if !isXMLSpace(string(t)) {
					return nil, errXML
				}
				continue
			}
			if top := stack[len(stack)-1]; len(top.children) == 0 {
				top.text += string(t)
			}
		case xml.Directive:
			return nil, errXML
		}
	}
	if root == nil || len(stack) > 0 {
		return nil, errXML
	}
	return root, nil
}
