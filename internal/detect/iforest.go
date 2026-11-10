package detect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"sort"
)

// Isolation Forest hyperparameters. These are fixed by the D2.2 evaluation and
// a model trained under different values must not be loaded by a hub compiled
// against these.
const (
	NumTrees  = 100
	Subsample = 256
	MaxDepth  = 8

	// Contamination is the fraction of the training set that sits above the
	// decision threshold. It sets the operating point on the precision/recall
	// curve; 0.001 was selected in D2.2 Section 6.
	Contamination = 0.001

	eulerMascheroni = 0.5772156649015329
)

// node is a single split in an isolation tree. A node with feature < 0 is a
// leaf and carries the number of training points that reached it.
type node struct {
	Feature int     `json:"f"`
	Split   float64 `json:"s,omitempty"`
	Left    *node   `json:"l,omitempty"`
	Right   *node   `json:"r,omitempty"`
	Size    int     `json:"n,omitempty"`
}

// Forest is a trained Isolation Forest for one sensor type.
type Forest struct {
	Trees     []*node `json:"trees"`
	Norm      float64 `json:"norm"`      // c(n) for the training subsample size
	Threshold float64 `json:"threshold"` // score at or above which a reading is anomalous
}

// cFactor is the average path length of an unsuccessful search in a binary
// search tree over n points. It normalises path lengths so that scores are
// comparable across subsample sizes.
func cFactor(n int) float64 {
	if n <= 1 {
		return 0
	}
	fn := float64(n)
	return 2*(math.Log(fn-1)+eulerMascheroni) - 2*(fn-1)/fn
}

// Train fits a forest on the supplied feature vectors. Training is offline: the
// Home Hub loads a serialised model rather than training in place.
func Train(data []Features, seed uint64) (*Forest, error) {
	if len(data) < Subsample {
		return nil, errors.New("detect: training set smaller than the subsample size")
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	f := &Forest{
		Trees: make([]*node, 0, NumTrees),
		Norm:  cFactor(Subsample),
	}
	idx := make([]int, len(data))
	for i := range idx {
		idx[i] = i
	}
	for t := 0; t < NumTrees; t++ {
		// Partial Fisher-Yates: the first Subsample entries become the sample.
		for i := 0; i < Subsample; i++ {
			j := i + rng.IntN(len(idx)-i)
			idx[i], idx[j] = idx[j], idx[i]
		}
		sample := make([]int, Subsample)
		copy(sample, idx[:Subsample])
		f.Trees = append(f.Trees, buildTree(data, sample, 0, rng))
	}

	// The decision threshold is the (1 - Contamination) quantile of the
	// training score distribution, so the operating point is defined by the
	// data rather than by an arbitrary constant.
	scores := make([]float64, len(data))
	for i := range data {
		scores[i] = f.Score(data[i])
	}
	f.Threshold = quantile(scores, 1-Contamination)
	return f, nil
}

func buildTree(data []Features, idx []int, depth int, rng *rand.Rand) *node {
	n := &node{Feature: -1, Size: len(idx)}
	if depth >= MaxDepth || len(idx) <= 1 {
		return n
	}

	// Try a bounded number of random features; a partition in which every
	// candidate feature is constant becomes a leaf.
	for attempt := 0; attempt < 10; attempt++ {
		feature := rng.IntN(NumFeatures)
		lo, hi := data[idx[0]][feature], data[idx[0]][feature]
		for _, i := range idx[1:] {
			v := data[i][feature]
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		if hi <= lo {
			continue
		}
		split := lo + rng.Float64()*(hi-lo)

		left := make([]int, 0, len(idx)/2)
		right := make([]int, 0, len(idx)/2)
		for _, i := range idx {
			if data[i][feature] < split {
				left = append(left, i)
			} else {
				right = append(right, i)
			}
		}
		if len(left) == 0 || len(right) == 0 {
			continue
		}
		n.Feature = feature
		n.Split = split
		n.Left = buildTree(data, left, depth+1, rng)
		n.Right = buildTree(data, right, depth+1, rng)
		return n
	}
	return n
}

// pathLength walks one tree and returns the isolation depth of x, adjusted by
// the expected remaining depth of the leaf it lands in.
func pathLength(n *node, x Features) float64 {
	depth := 0
	for n.Feature >= 0 {
		if x[n.Feature] < n.Split {
			n = n.Left
		} else {
			n = n.Right
		}
		depth++
	}
	return float64(depth) + cFactor(n.Size)
}

// Score returns the isolation score of a feature vector in (0, 1]. Scores near
// 1 indicate points that isolate in very few splits and are therefore anomalous;
// scores near 0.5 indicate points that sit inside the learned distribution.
func (f *Forest) Score(x Features) float64 {
	if len(f.Trees) == 0 {
		return 0
	}
	total := 0.0
	for _, t := range f.Trees {
		total += pathLength(t, x)
	}
	mean := total / float64(len(f.Trees))
	return math.Pow(2, -mean/(f.Norm+epsilon))
}

// IsAnomalous applies the forest's trained decision threshold.
func (f *Forest) IsAnomalous(x Features) bool {
	return f.Score(x) >= f.Threshold
}

func quantile(values []float64, q float64) float64 {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	k := int(q * float64(len(sorted)-1))
	if k < 0 {
		k = 0
	}
	if k >= len(sorted) {
		k = len(sorted) - 1
	}
	return sorted[k]
}

// Model is the serialised on-disk form: one forest per quantitative sensor type,
// keyed by protocol.DataType.
type Model struct {
	Version string             `json:"version"`
	Forests map[string]*Forest `json:"forests"`
}

// WriteModel serialises a trained model.
func WriteModel(w io.Writer, m *Model) error {
	return json.NewEncoder(w).Encode(m)
}

// ReadModel loads a serialised model. The tree structure is validated on load
// because Score walks it without bounds checks: a truncated or hand-edited model
// file would otherwise panic inside the hub's detection goroutine on the first
// reading it scored. Rejecting it here turns a crash into the documented
// degradation to the deterministic and adaptive layers.
func ReadModel(r io.Reader) (*Model, error) {
	var m Model
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, err
	}
	if len(m.Forests) == 0 {
		return nil, errors.New("detect: model contains no forests")
	}
	for dataType, f := range m.Forests {
		if f == nil {
			return nil, fmt.Errorf("detect: forest %q is null", dataType)
		}
		for i, t := range f.Trees {
			if err := validateNode(t); err != nil {
				return nil, fmt.Errorf("detect: forest %q tree %d: %w", dataType, i, err)
			}
		}
	}
	return &m, nil
}

// validateNode checks the invariants pathLength relies on: a split node's
// feature index is within the feature vector and both of its children exist.
func validateNode(n *node) error {
	if n == nil {
		return errors.New("missing node")
	}
	if n.Feature < 0 {
		return nil
	}
	if n.Feature >= NumFeatures {
		return fmt.Errorf("feature index %d out of range", n.Feature)
	}
	if err := validateNode(n.Left); err != nil {
		return err
	}
	return validateNode(n.Right)
}
