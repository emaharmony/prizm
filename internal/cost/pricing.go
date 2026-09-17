package cost

import "sync"

// PricingTable provides estimated INPUT (prompt) cost per 1K tokens for known
// models, in USD. OutputPricingTable holds the COMPLETION price per 1K tokens.
// These are approximate built-in defaults and act as the fallback when no
// config override is supplied. Operators can override or extend them at runtime
// via SetPricingOverrides (wired from prizm.yaml's `cost.pricing`) so prices can
// be corrected without recompiling.
//
// EstimateCost (input-only, total-token) is kept for backward compatibility with
// callers that only have a total token count. EstimateCostSplit is preferred when
// prompt and completion counts are known, since output tokens are usually priced
// several times higher than input tokens.
var PricingTable = map[string]float64{
	// OpenAI
	"gpt-4o":        0.0025,
	"gpt-4o-mini":   0.00015,
	"gpt-4-turbo":   0.01,
	"gpt-4":         0.03,
	"gpt-3.5-turbo": 0.0005,

	// Anthropic
	"claude-opus-4-20250514":     0.015,
	"claude-sonnet-4-20250514":   0.003,
	"claude-3-7-sonnet-20250219": 0.003,
	"claude-3-5-sonnet-20241022": 0.003,
	"claude-3-5-haiku-20241022":  0.0008,
	"claude-3-haiku-20240307":    0.00025,

	// Gemini
	"gemini-2.0-flash": 0.0001,
	"gemini-1.5-pro":   0.00125,
	"gemini-1.5-flash": 0.000075,

	// Ollama (local — free)
	"llama3":      0.0,
	"llama3.1":    0.0,
	"mistral":     0.0,
	"codellama":   0.0,
	"qwen2":       0.0,
	"gemma2":      0.0,
	"phi3":        0.0,
	"deepseek-v2": 0.0,
	"glm-4":       0.0,
}

// OutputPricingTable provides estimated COMPLETION (output) cost per 1K tokens.
// Models absent here fall back to their input price (a conservative under-count
// for most paid models, and correct 0.0 for local ones).
var OutputPricingTable = map[string]float64{
	// OpenAI
	"gpt-4o":        0.01,
	"gpt-4o-mini":   0.0006,
	"gpt-4-turbo":   0.03,
	"gpt-4":         0.06,
	"gpt-3.5-turbo": 0.0015,

	// Anthropic
	"claude-opus-4-20250514":     0.075,
	"claude-sonnet-4-20250514":   0.015,
	"claude-3-7-sonnet-20250219": 0.015,
	"claude-3-5-sonnet-20241022": 0.015,
	"claude-3-5-haiku-20241022":  0.004,
	"claude-3-haiku-20240307":    0.00125,

	// Gemini
	"gemini-2.0-flash": 0.0004,
	"gemini-1.5-pro":   0.005,
	"gemini-1.5-flash": 0.0003,
}

// ModelPrice is a per-model pricing override in USD per 1K tokens. It carries
// both the input (prompt) and output (completion) price so operators can supply
// a full override from config. It maps 1:1 to prizm.yaml's `cost.pricing.<model>`.
type ModelPrice struct {
	Input  float64
	Output float64
}

// Pricer estimates token cost from an input/output price table. It is the
// config-aware core behind the package-level EstimateCost/EstimateCostSplit
// helpers. Construct one with NewPricer to layer overrides over the built-ins.
type Pricer struct {
	input  map[string]float64
	output map[string]float64
}

// NewPricer returns a Pricer seeded with the built-in PricingTable /
// OutputPricingTable and then layered with overrides. An override sets both the
// input and output price for that model; a model present only in overrides is
// added. Passing a nil/empty map yields the built-in defaults.
func NewPricer(overrides map[string]ModelPrice) *Pricer {
	in := make(map[string]float64, len(PricingTable)+len(overrides))
	out := make(map[string]float64, len(OutputPricingTable)+len(overrides))
	for k, v := range PricingTable {
		in[k] = v
	}
	for k, v := range OutputPricingTable {
		out[k] = v
	}
	for model, price := range overrides {
		in[model] = price.Input
		out[model] = price.Output
	}
	return &Pricer{input: in, output: out}
}

// EstimateCost calculates the estimated cost for a given model and total token
// count using input pricing only. Returns 0.0 for unknown (assumed local/free)
// models.
func (p *Pricer) EstimateCost(model string, totalTokens int) float64 {
	pricePer1K, ok := p.input[model]
	if !ok {
		return 0.0 // Unknown model, assume free/local
	}
	return float64(totalTokens) / 1000.0 * pricePer1K
}

// EstimateCostSplit calculates the estimated cost given separate prompt and
// completion token counts, using input pricing for prompt tokens and output
// pricing for completion tokens. Unknown models return 0.0 (assumed local/free).
// A model priced for input but missing an output price falls back to its input
// price for completion tokens.
func (p *Pricer) EstimateCostSplit(model string, promptTokens, completionTokens int) float64 {
	inPer1K, ok := p.input[model]
	if !ok {
		return 0.0 // Unknown model, assume free/local
	}
	outPer1K, ok := p.output[model]
	if !ok {
		outPer1K = inPer1K
	}
	return float64(promptTokens)/1000.0*inPer1K + float64(completionTokens)/1000.0*outPer1K
}

// defaultPricer backs the package-level EstimateCost/EstimateCostSplit helpers.
// It starts from the built-in tables and can be replaced at startup by
// SetPricingOverrides. Guarded by defaultPricerMu so a startup override cannot
// race the provider registry's usage recorder.
var (
	defaultPricerMu sync.RWMutex
	defaultPricer   = NewPricer(nil)
)

// SetPricingOverrides rebuilds the package-level pricer with config overrides
// layered over the built-in tables. Call once at startup (e.g. from `prizm
// serve`) with prizm.yaml's `cost.pricing`. A nil/empty map restores defaults.
func SetPricingOverrides(overrides map[string]ModelPrice) {
	p := NewPricer(overrides)
	defaultPricerMu.Lock()
	defaultPricer = p
	defaultPricerMu.Unlock()
}

// EstimateCost calculates the estimated cost for a given model and total token
// count using input pricing only. Returns 0.0 for unknown (assumed local/free)
// models. Prefer EstimateCostSplit when prompt/completion counts are available.
func EstimateCost(model string, totalTokens int) float64 {
	defaultPricerMu.RLock()
	p := defaultPricer
	defaultPricerMu.RUnlock()
	return p.EstimateCost(model, totalTokens)
}

// EstimateCostSplit calculates the estimated cost given separate prompt and
// completion token counts. See Pricer.EstimateCostSplit.
func EstimateCostSplit(model string, promptTokens, completionTokens int) float64 {
	defaultPricerMu.RLock()
	p := defaultPricer
	defaultPricerMu.RUnlock()
	return p.EstimateCostSplit(model, promptTokens, completionTokens)
}
