package classifier

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

type Model struct {
	Version       int            `json:"version"`
	Vocab         map[string]int `json:"vocab"`
	IDF           []float64      `json:"idf"`
	Weights       [][]float64    `json:"weights"`
	Bias          []float64      `json:"bias"`
	Categories    []string       `json:"categories"`
	TrainedAt     string         `json:"trained_at"`
	TrainingCount int            `json:"training_count"`
}

type Sample struct {
	Text       string
	CategoryID string
}

func Train(samples []Sample, version int, trainedAt string) (*Model, error) {
	if len(samples) == 0 {
		return nil, fmt.Errorf("classifier: no training data")
	}
	df := map[string]int{}
	docs := make([]map[string]int, len(samples))
	for i, s := range samples {
		tf := map[string]int{}
		for _, tok := range Tokenize(s.Text) {
			tf[tok]++
		}
		docs[i] = tf
		for tok := range tf {
			df[tok]++
		}
	}
	type term struct {
		tok string
		df  int
	}
	terms := make([]term, 0, len(df))
	for tok, n := range df {
		if n >= 2 {
			terms = append(terms, term{tok, n})
		}
	}
	sort.Slice(terms, func(i, j int) bool { return terms[i].df > terms[j].df })
	if len(terms) > 20000 {
		terms = terms[:20000]
	}
	vocab := map[string]int{}
	for i, t := range terms {
		vocab[t.tok] = i
	}
	classIdx := map[string]int{}
	classes := []string{}
	for _, s := range samples {
		if _, ok := classIdx[s.CategoryID]; !ok {
			classIdx[s.CategoryID] = len(classes)
			classes = append(classes, s.CategoryID)
		}
	}
	K := len(classes)
	V := len(vocab)
	if V == 0 || K == 0 {
		return nil, fmt.Errorf("classifier: empty features or classes")
	}
	idf := make([]float64, V)
	N := float64(len(samples))
	for tok, idx := range vocab {
		idf[idx] = math.Log((N + 1) / (float64(df[tok]) + 1))
	}
	feats := make([][]feature, len(samples))
	labels := make([]int, len(samples))
	for i, s := range samples {
		feats[i] = featurizeSparse(docs[i], vocab, idf)
		labels[i] = classIdx[s.CategoryID]
	}
	W := make([][]float64, K)
	for k := range W {
		W[k] = make([]float64, V)
	}
	bias := make([]float64, K)

	mW := make([][]float64, K)
	vW := make([][]float64, K)
	for k := range W {
		mW[k] = make([]float64, V)
		vW[k] = make([]float64, V)
	}
	mB := make([]float64, K)
	vB := make([]float64, K)
	lr, b1, b2, eps, lambda := 0.05, 0.9, 0.999, 1e-8, 1e-4
	epochs := 30
	batch := 32
	t := 0
	idxs := make([]int, len(samples))
	for i := range idxs {
		idxs[i] = i
	}
	seedShuffle(idxs)

	for epoch := 0; epoch < epochs; epoch++ {
		seedShuffle(idxs)
		for start := 0; start < len(idxs); start += batch {
			end := start + batch
			if end > len(idxs) {
				end = len(idxs)
			}
			gW := make([][]float64, K)
			for k := range gW {
				gW[k] = make([]float64, V)
			}
			gB := make([]float64, K)
			for _, i := range idxs[start:end] {
				probs := softmax(feats[i], W, bias)
				y := labels[i]
				for k := 0; k < K; k++ {
					d := probs[k]
					if k == y {
						d -= 1
					}
					if d == 0 {
						continue
					}
					gB[k] += d
					for _, f := range feats[i] {
						gW[k][f.i] += d * f.v
					}
				}
			}
			n := float64(end - start)
			t++
			b1t := 1 - math.Pow(b1, float64(t))
			b2t := 1 - math.Pow(b2, float64(t))
			for k := 0; k < K; k++ {
				gB[k] /= n
				mB[k] = b1*mB[k] + (1-b1)*gB[k]
				vB[k] = b2*vB[k] + (1-b2)*gB[k]*gB[k]
				bias[k] -= lr * (mB[k] / b1t) / (math.Sqrt(vB[k]/b2t) + eps)
				for _, f := range nz(gW[k]) {
					g := gW[k][f.i]/n + lambda*W[k][f.i]
					mW[k][f.i] = b1*mW[k][f.i] + (1-b1)*g
					vW[k][f.i] = b2*vW[k][f.i] + (1-b2)*g*g
					W[k][f.i] -= lr * (mW[k][f.i] / b1t) / (math.Sqrt(vW[k][f.i]/b2t) + eps)
				}
			}
		}
	}
	return &Model{
		Version: version, Vocab: vocab, IDF: idf, Weights: W, Bias: bias,
		Categories: classes, TrainedAt: trainedAt, TrainingCount: len(samples),
	}, nil
}

type feature struct {
	i int
	v float64
}

func nz(vec []float64) []feature {
	out := []feature{}
	for i, v := range vec {
		if v != 0 {
			out = append(out, feature{i, v})
		}
	}
	return out
}

func featurizeSparse(tf map[string]int, vocab map[string]int, idf []float64) []feature {
	pairs := make([]feature, 0, len(tf))
	norm := 0.0
	for tok, n := range tf {
		idx, ok := vocab[tok]
		if !ok {
			continue
		}
		v := (1 + math.Log(float64(n))) * idf[idx]
		pairs = append(pairs, feature{idx, v})
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range pairs {
			pairs[i].v /= norm
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].i < pairs[j].i })
	return pairs
}

func softmax(x []feature, W [][]float64, bias []float64) []float64 {
	K := len(bias)
	out := make([]float64, K)
	max := math.Inf(-1)
	for k := 0; k < K; k++ {
		z := bias[k]
		for _, f := range x {
			z += W[k][f.i] * f.v
		}
		out[k] = z
		if z > max {
			max = z
		}
	}
	sum := 0.0
	for k := range out {
		out[k] = math.Exp(out[k] - max)
		sum += out[k]
	}
	if sum == 0 {
		sum = 1
	}
	for k := range out {
		out[k] /= sum
	}
	return out
}

type Prediction struct {
	CategoryID string
	Confidence float64
}

func (m *Model) Predict(text string, allowed map[string]bool) []Prediction {
	tf := map[string]int{}
	for _, tok := range Tokenize(text) {
		tf[tok]++
	}
	x := featurizeSparse(tf, m.Vocab, m.IDF)
	candIdx := []int{}
	for k, c := range m.Categories {
		if allowed == nil || allowed[c] {
			candIdx = append(candIdx, k)
		}
	}
	if len(candIdx) == 0 {
		return nil
	}
	all := softmax(x, m.Weights, m.Bias)
	var sum float64
	for _, k := range candIdx {
		sum += all[k]
	}
	if sum <= 0 {
		return nil
	}
	out := make([]Prediction, 0, len(candIdx))
	for _, k := range candIdx {
		out = append(out, Prediction{CategoryID: m.Categories[k], Confidence: all[k] / sum})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}

func Tokenize(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	out := make([]string, 0, len(fields)*2)
	out = append(out, fields...)
	for i := 0; i+1 < len(fields); i++ {
		out = append(out, fields[i]+"_"+fields[i+1])
	}
	return out
}

func (m *Model) Encode() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeModel(payload string) (*Model, error) {
	var m Model
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func seedShuffle(idx []int) {
	s := uint32(12345)
	for i := len(idx) - 1; i > 0; i-- {
		s = s*1664525 + 1013904223
		j := int(s>>8) % (i + 1)
		idx[i], idx[j] = idx[j], idx[i]
	}
}
