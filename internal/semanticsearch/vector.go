package semanticsearch

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

const DefaultEmbeddingDimensions = 768

func ContentHash(scope string, chatID int64, messageID int, text string) string {
	payload := fmt.Sprintf("%s\x00%d\x00%d\x00%s", strings.TrimSpace(scope), chatID, messageID, strings.TrimSpace(text))
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

func EncodeFloat32(values []float32) []byte {
	data := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(data[index*4:], math.Float32bits(value))
	}
	return data
}

func DecodeFloat32(data []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be positive")
	}
	if len(data) != dimensions*4 {
		return nil, fmt.Errorf("embedding byte length %d does not match %d dimensions", len(data), dimensions)
	}
	values := make([]float32, dimensions)
	for index := range values {
		values[index] = math.Float32frombits(binary.LittleEndian.Uint32(data[index*4:]))
	}
	return values, nil
}

func Normalize(values []float32) ([]float32, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("embedding is empty")
	}
	var squared float64
	for _, value := range values {
		squared += float64(value) * float64(value)
	}
	if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
		return nil, fmt.Errorf("embedding norm is invalid")
	}
	norm := float32(math.Sqrt(squared))
	out := make([]float32, len(values))
	for index, value := range values {
		out[index] = value / norm
	}
	return out, nil
}

func Cosine(left, right []float32) (float64, error) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, fmt.Errorf("embedding dimensions do not match: %d and %d", len(left), len(right))
	}
	var dot, leftSquared, rightSquared float64
	for index := range left {
		l, r := float64(left[index]), float64(right[index])
		dot += l * r
		leftSquared += l * l
		rightSquared += r * r
	}
	if leftSquared == 0 || rightSquared == 0 {
		return 0, fmt.Errorf("embedding norm is zero")
	}
	return dot / (math.Sqrt(leftSquared) * math.Sqrt(rightSquared)), nil
}

func CompactSnippet(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	if limit == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
