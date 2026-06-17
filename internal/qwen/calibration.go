package qwen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func LoadJSONL(path string) ([]MessageInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []MessageInput
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 1024*1024)
	scanner.Buffer(buffer, 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var input MessageInput
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		out = append(out, input)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ParseBatchSizes(value string) ([]int, error) {
	if strings.TrimSpace(value) == "" {
		return []int{4, 8, 12, 16, 24}, nil
	}
	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return nil, fmt.Errorf("batch size must be positive")
		}
		out = append(out, n)
	}
	return out, nil
}

func RunCalibration(ctx context.Context, client *Client, inputs []MessageInput, batchSizes []int, maxChars int, outPath string) ([]CalibrationResult, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no calibration inputs")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	results := make([]CalibrationResult, 0, len(batchSizes))
	for _, size := range batchSizes {
		batch := takeBatch(inputs, size, maxChars)
		result := CalibrationResult{
			BatchSize:     size,
			InputMessages: len(batch),
			InputChars:    ApproxChars(batch),
		}
		started := time.Now()
		response, _, err := client.ClassifyBatch(ctx, batch)
		result.DurationMillis = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = err.Error()
		} else {
			result.JSONValid = true
			for _, decision := range response.Decisions {
				if decision.Keep {
					result.Kept++
				}
				if decision.Importance >= 2 {
					result.Important++
				}
				if decision.HasEvent {
					result.Events++
				}
			}
		}
		if err := encoder.Encode(result); err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func takeBatch(inputs []MessageInput, size int, maxChars int) []MessageInput {
	if size > len(inputs) {
		size = len(inputs)
	}
	out := make([]MessageInput, 0, size)
	for _, input := range inputs {
		if len(out) >= size {
			break
		}
		candidate := append(out, input)
		if maxChars > 0 && ApproxChars(candidate) > maxChars && len(out) > 0 {
			break
		}
		out = candidate
	}
	return out
}

func DefaultOutputPath(stateDir string) string {
	return filepath.Join(stateDir, "artifacts", "qwen-calibration-"+time.Now().UTC().Format("20060102T150405Z")+".jsonl")
}
