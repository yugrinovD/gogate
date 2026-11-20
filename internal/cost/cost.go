// Package cost computes request cost from token usage.
package cost

// Price is USD per one million tokens.
type Price struct {
	Input  float64
	Output float64
}

// Calculate returns the cost in USD.
func Calculate(p Price, promptTokens, completionTokens int) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	return float64(promptTokens)/1_000_000*p.Input + float64(completionTokens)/1_000_000*p.Output
}
