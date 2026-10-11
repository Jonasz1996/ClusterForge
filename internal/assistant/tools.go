package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Tool is een tool van het model: een vaste vraag aan ClusterForge die
// alleen leest, met de rechten van wie de vraag stelt.
type Tool struct {
	Name        string
	Description string
	// Properties en Required vormen het invoerschema. Het schema is strict:
	// andere velden mogen niet.
	Properties map[string]any
	Required   []string
	// Label zegt in het Nederlands wat de tool met deze invoer doet, zoals
	// 'Tijdlijn van web-prod lezen'. De webinterface toont het terwijl hij
	// loopt.
	Label func(in map[string]any) string
	// Run geeft het resultaat; het gaat als compacte JSON naar het model.
	Run func(ctx context.Context, c Caller, in json.RawMessage) (any, error)
}

// ToolError is een fout die het model te zien krijgt, zoals een onbekend
// cluster. Andere fouten worden gelogd en het model krijgt 'interne fout'.
type ToolError struct{ Msg string }

func (e ToolError) Error() string { return e.Msg }

// toolTimeout is hoe lang één tool mag lezen.
const toolTimeout = 20 * time.Second

func (t Tool) param() anthropic.BetaToolUnionParam {
	props := t.Properties
	if props == nil {
		props = map[string]any{}
	}
	return anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
		Name: t.Name, Description: anthropic.String(t.Description), Strict: anthropic.Bool(true),
		// De invoer komt meteen door; Decode controleert hem daarna.
		EagerInputStreaming: anthropic.Bool(true),
		InputSchema: anthropic.BetaToolInputSchemaParam{
			Properties: props, Required: t.Required, ExtraFields: map[string]any{"additionalProperties": false},
		},
	}}
}

// Decode leest de invoer van een tool in v en weigert onbekende velden.
func Decode(in json.RawMessage, v any) error {
	if len(bytes.TrimSpace(in)) == 0 {
		in = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(in))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ToolError{"ongeldige invoer: " + err.Error()}
	}
	return nil
}

func (s *Service) tool(name string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// label geeft de zin bij een aanroep, ook voor een tool die niet meer
// bestaat.
func (s *Service) label(name string, input json.RawMessage) string {
	t, ok := s.tool(name)
	if !ok || t.Label == nil {
		return name
	}
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	if in == nil {
		in = map[string]any{}
	}
	return t.Label(in)
}

// runTool voert een tool uit en geeft het resultaat als tekst, en of het
// een fout is.
func (s *Service) runTool(ctx context.Context, c Caller, name string, input json.RawMessage) (string, bool) {
	t, ok := s.tool(name)
	if !ok {
		return "onbekende tool " + name, true
	}
	// De invoer stroomt in stukken binnen; een afgebroken aanroep mist dan
	// velden. Die gaat terug naar het model in plaats van met nullen te lopen.
	var fields map[string]json.RawMessage
	if len(bytes.TrimSpace(input)) > 0 {
		if err := json.Unmarshal(input, &fields); err != nil {
			return "ongeldige invoer: geen JSON-object", true
		}
	}
	for _, k := range t.Required {
		if _, ok := fields[k]; !ok {
			return "onvolledige invoer: " + k + " ontbreekt", true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, err := t.Run(ctx, c, input)
	var te ToolError
	switch {
	case errors.As(err, &te):
		return te.Msg, true
	case err != nil:
		s.log.ErrorContext(ctx, "tool van de assistent mislukt", "tool", name, "err", err)
		return "interne fout bij het lezen", true
	}
	b, err := json.Marshal(out)
	if err != nil {
		s.log.ErrorContext(ctx, "resultaat van tool niet te schrijven", "tool", name, "err", err)
		return "interne fout bij het lezen", true
	}
	return truncate(string(b)), false
}

// truncate kapt een resultaat af op MaxToolResult en zegt dat erbij.
func truncate(s string) string {
	if len(s) <= MaxToolResult {
		return s
	}
	cut := MaxToolResult - 200
	// Niet midden in een UTF-8-teken.
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[afgekapt: het resultaat was %d KB; vraag een kortere periode of gebruik een filter]", len(s)/1024)
}
