package semanticsearch

import (
	"math"
	"reflect"
	"testing"
)

func TestFloat32RoundTripAndCosine(t *testing.T) {
	want := []float32{1, 2, 3}
	got, err := DecodeFloat32(EncodeFloat32(want), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v", got)
	}
	score, err := Cosine(want, want)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(score-1) > 1e-6 {
		t.Fatalf("cosine = %f", score)
	}
}

func TestNormalizeRejectsZeroVector(t *testing.T) {
	if _, err := Normalize([]float32{0, 0}); err == nil {
		t.Fatal("expected zero-vector error")
	}
}

func TestCompactSnippet(t *testing.T) {
	if got := CompactSnippet("  один\n два   три ", 9); got != "один два…" {
		t.Fatalf("snippet = %q", got)
	}
}
