package assistant

import "strings"

// Price is wat een model kost per miljoen tokens, in dollar. Schrijven naar
// de cache kost 1,25 keer de invoer.
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
}

// Model is een model dat een beheerder kan kiezen.
type Model struct {
	ID    string
	Label string
	Price Price
	// Fallback is true als de API een geweigerde vraag op een ander model
	// kan herhalen. Haiku kan dat niet; daar geeft de parameter een 400.
	Fallback bool
}

// DefaultModel is het model zolang een beheerder niets anders kiest.
const DefaultModel = "claude-opus-5-5"

// Models zijn de modellen in de instellingen, het sterkste eerst.
var Models = []Model{
	{ID: "claude-opus-5-5", Label: "Claude Opus 5.5", Price: Price{4, 20, 0.20, 5}, Fallback: true},
	{ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", Price: Price{2, 10, 0.20, 2.5}, Fallback: true},
	{ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5", Price: Price{0.10, 0.50, 0.01, 0.125}},
}

// fallbackPrices zijn de modellen waarop de API een geweigerde vraag kan
// herhalen; hun antwoord kost hun eigen prijs.
var fallbackPrices = map[string]Price{
	"claude-opus-5":   {5, 25, 0.50, 6.25},
	"claude-opus-4-8": {5, 25, 0.50, 6.25},
	"claude-sonnet-5": {2, 10, 0.20, 2.5},
}

func modelByID(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// priceOf geeft de prijs van het model dat antwoordde. Een onbekend model
// rekent met de prijs van het gekozen model, zodat het budget blijft tellen.
func priceOf(model, chosen string) Price {
	for _, m := range Models {
		if model == m.ID || strings.HasPrefix(model, m.ID+"-") {
			return m.Price
		}
	}
	for id, p := range fallbackPrices {
		if model == id || strings.HasPrefix(model, id+"-") {
			return p
		}
	}
	if m, ok := modelByID(chosen); ok {
		return m.Price
	}
	return Models[0].Price
}

// Tokens is het verbruik van één antwoord van de API.
type Tokens struct {
	Input, Output, CacheRead, CacheWrite int64
}

func (u *Tokens) add(o Tokens) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

// cost is de prijs in miljoensten van een dollar.
func cost(p Price, u Tokens) int64 {
	// Per miljoen tokens in dollar is per token in miljoensten van een dollar.
	f := float64(u.Input)*p.Input + float64(u.Output)*p.Output + float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite)*p.CacheWrite
	return int64(f + 0.5)
}

// USD zet miljoensten om naar dollars.
func USD(micro int64) float64 { return float64(micro) / 1e6 }
