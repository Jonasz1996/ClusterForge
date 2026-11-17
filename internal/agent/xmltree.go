package agent

// Een klein XML-boommodel voor config.xml van OPNsense (fase 34). De
// elementnamen van interfaces (lan, wan, opt1) zijn vrij, dus een vaste
// struct past niet.

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

type xmlNode struct {
	Name     string
	Attr     []xml.Attr
	Text     string
	Children []*xmlNode
}

// parseXMLTree leest een XML-document als boom; de wortel is het eerste
// element.
func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var stack []*xmlNode
	var root *xmlNode
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
			n := &xmlNode{Name: t.Name.Local, Attr: t.Attr}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return root, nil
}

// child geeft het eerste kind met deze naam, of nil.
func (n *xmlNode) child(name string) *xmlNode {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// path volgt een pad als "system/webgui/port".
func (n *xmlNode) path(p string) *xmlNode {
	for part := range strings.SplitSeq(p, "/") {
		n = n.child(part)
	}
	return n
}

// text is de getrimde tekst van een pad; leeg als het er niet is.
func (n *xmlNode) text(p string) string {
	if c := n.path(p); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

// all geeft alle kinderen met deze naam.
func (n *xmlNode) all(name string) []*xmlNode {
	if n == nil {
		return nil
	}
	var out []*xmlNode
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}
