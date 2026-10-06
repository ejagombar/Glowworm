package classifier

import (
	"testing"

	"glowworm/internal/core/normalize"
)

func TestTrainPredictRoundtrip(t *testing.T) {
	samples := []Sample{}
	groceries := []string{"tesco express", "tesco stores", "sainsburys local", "asda superstore", "morrisons daily", "aldi"}
	transport := []string{"transport for london", "uber trip", "tfl travel charge", "national rail", "uber ride", "lime scooter"}
	for i := 0; i < 8; i++ {
		for _, g := range groceries {
			samples = append(samples, Sample{Text: normalize.Text(g + " 452" + string(rune('a'+i%10))), CategoryID: "food"})
		}
		for _, tr := range transport {
			samples = append(samples, Sample{Text: normalize.Text(tr), CategoryID: "transport"})
		}
	}
	m, err := Train(samples, 1, "now")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Vocab) == 0 || len(m.Categories) != 2 {
		t.Fatalf("bad model: vocab=%d classes=%v", len(m.Vocab), m.Categories)
	}
	food := m.Predict("tesco express 4211", map[string]bool{"food": true, "transport": true})
	if len(food) == 0 || food[0].CategoryID != "food" || food[0].Confidence < 0.75 {
		t.Errorf("food prediction = %+v", food)
	}
	transportPred := m.Predict("uber trip help.uber.com", map[string]bool{"food": true, "transport": true})
	if len(transportPred) == 0 || transportPred[0].CategoryID != "transport" || transportPred[0].Confidence < 0.75 {
		t.Errorf("transport prediction = %+v", transportPred)
	}
	only := m.Predict("tesco express", map[string]bool{"transport": true})
	if len(only) != 1 || only[0].CategoryID != "transport" || only[0].Confidence != 1 {
		t.Errorf("masked prediction = %+v", only)
	}
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := DecodeModel(enc)
	if err != nil {
		t.Fatal(err)
	}
	p2 := m2.Predict("uber trip", map[string]bool{"food": true, "transport": true})
	if len(p2) == 0 || p2[0].CategoryID != "transport" {
		t.Errorf("decoded prediction = %+v", p2)
	}
}
