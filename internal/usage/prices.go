package usage

import (
	"sort"
	"strings"
)

// PricesAsOf says when the price table was last checked.
const PricesAsOf = "November 2026"

// Price is a model's list price in USD per million tokens. CacheWrite is the
// 5-minute cache write price (1.25x input).
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
	// Above, when set, applies to requests whose prompt exceeds AboveTokens.
	AboveTokens int
	Above       *Price
}

// Anthropic first-party list prices. Keys are model id prefixes, so dated
// ids (claude-haiku-4-5-20251001) match too; the longest prefix wins.
var prices = map[string]Price{
	"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite: 10},
	"claude-mythos-5-2": {Input: 8, Output: 40, CacheRead: 0.20, CacheWrite: 10},
	"claude-fable-5-1":  {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5},
	"claude-mythos-5-1": {Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5},
	"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1.00, CacheWrite: 12.5},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20, CacheWrite: 5},
	"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-8":   {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-7":   {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-6":   {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-5":   {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-1":   {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite: 18.75},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10, CacheWrite: 2.5},
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.20, CacheWrite: 2.5},
	"claude-sonnet-4-6": {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
	"claude-sonnet-4-5": {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
	"claude-haiku-5-5": {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125,
		AboveTokens: 100_000, Above: &Price{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite: 0.625}},
	"claude-haiku-4-5": {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite: 1.25},
}

var priceKeys = func() []string {
	keys := make([]string, 0, len(prices))
	for k := range prices {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}()

// PriceFor returns the list price of a model; ok is false for unknown models
// (local models, other providers). Platform prefixes such as
// "anthropic." or "us.anthropic." are ignored.
func PriceFor(model string) (Price, bool) {
	m := strings.ToLower(model)
	if i := strings.Index(m, "claude-"); i > 0 {
		m = m[i:]
	}
	for _, k := range priceKeys {
		if strings.HasPrefix(m, k) {
			return prices[k], true
		}
	}
	return Price{}, false
}

// Tokens are one request's counts; Input excludes cached tokens.
type Tokens struct {
	Input, Output, CacheRead, CacheWrite int64
}

// Cost estimates the USD cost of one request.
func (p Price) Cost(t Tokens) float64 {
	if p.Above != nil && t.Input+t.CacheRead+t.CacheWrite > int64(p.AboveTokens) {
		return p.Above.Cost(t)
	}
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.CacheRead + float64(t.CacheWrite)*p.CacheWrite) / 1e6
}
