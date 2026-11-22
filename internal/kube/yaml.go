package kube

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ToYAML zet een object van de API om in YAML zoals kubectl het toont, met
// de velden in de volgorde van de API: apiVersion, kind, metadata, spec en
// status.
func ToYAML(body []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	n, err := yamlNode(dec)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	return b.String(), enc.Close()
}

// yamlNode leest één JSON-waarde en maakt er een YAML-knoop van, met de
// sleutels in de volgorde waarin ze staan.
func yamlNode(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("sleutel %v is geen tekst", kt)
				}
				v, err := yamlNode(dec)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, str(k), v)
			}
			_, err := dec.Token()
			return n, err
		case '[':
			n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for dec.More() {
				v, err := yamlNode(dec)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, v)
			}
			_, err := dec.Token()
			return n, err
		}
		return nil, fmt.Errorf("onverwacht %v", t)
	case string:
		return str(t), nil
	case json.Number:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: numberTag(t), Value: t.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(t)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("onverwacht %v", tok)
}

// yaml11 zijn woorden die YAML 1.1 als ja, nee of niets leest. kubectl
// leest YAML zo, dus krijgen ze aanhalingstekens.
var yaml11 = map[string]bool{}

func init() {
	for _, w := range strings.Fields("y yes on n no off true false null ~") {
		yaml11[w] = true
	}
}

func str(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	switch {
	case strings.Contains(v, "\n"):
		n.Style = yaml.LiteralStyle
	case yaml11[strings.ToLower(v)]:
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func numberTag(n json.Number) string {
	if strings.ContainsAny(n.String(), ".eE") {
		return "!!float"
	}
	return "!!int"
}
