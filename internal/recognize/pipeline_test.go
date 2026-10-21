package recognize

import (
	"testing"

	"ledger-snap/internal/openrouter"
)

func TestPickAmount(t *testing.T) {
	// An Apple Pay sheet: the amount, the pay button and the total all show 33.20.
	cands := []AmountCandidate{
		{Key: "a0", Value: "33.20"}, {Key: "a1", Value: "33.20"}, {Key: "a2", Value: "33.20"}, {Key: "a3", Value: "5.00"},
	}
	ans := openrouter.Answer{Choice: "a0", Probabilities: map[string]float64{"a0": 0.47, "a1": 0.39, "a2": 0.13, "a3": 0, "none": 0.01}}
	if money, p := pickAmount(cands, ans); money != "33.20" || p < 0.98 {
		t.Errorf("got %s %.2f, want 33.20 0.99", money, p)
	}

	// The split rows can outweigh a single row the model picked.
	ans = openrouter.Answer{Choice: "a3", Probabilities: map[string]float64{"a0": 0.3, "a1": 0.3, "a3": 0.4}}
	if money, _ := pickAmount(cands, ans); money != "33.20" {
		t.Errorf("got %s, want 33.20", money)
	}

	if money, p := pickAmount(nil, openrouter.Answer{Choice: "none", Probabilities: map[string]float64{"none": 1}}); money != "" || p != 0 {
		t.Errorf("got %q %.2f with no candidates", money, p)
	}
}
