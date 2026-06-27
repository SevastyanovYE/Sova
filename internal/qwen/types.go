package qwen

import (
	"errors"
	"fmt"
	"time"
)

type IncompleteResultError struct {
	Kind     string
	Returned int
	Expected int
}

func (e *IncompleteResultError) Error() string {
	return fmt.Sprintf("model returned %d %s for %d inputs", e.Returned, e.Kind, e.Expected)
}

func IsIncompleteResult(err error) bool {
	var target *IncompleteResultError
	return errors.As(err, &target)
}

type MessageInput struct {
	ID              string `json:"id"`
	SourceRef       string `json:"source_ref"`
	Kind            string `json:"kind"`
	Text            string `json:"text"`
	ExtractedText   string `json:"extracted_text,omitempty"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

type MessageDecision struct {
	ID         string   `json:"id"`
	Keep       bool     `json:"keep"`
	Importance int      `json:"importance"`
	Reason     string   `json:"reason"`
	Tags       []string `json:"tags"`
	HasEvent   bool     `json:"has_event"`
}

type BatchResult struct {
	Decisions []MessageDecision `json:"decisions"`
}

type ResponseMetrics struct {
	Model              string
	TotalDuration      time.Duration
	LoadDuration       time.Duration
	PromptEvalCount    int
	PromptEvalDuration time.Duration
	EvalCount          int
	EvalDuration       time.Duration
}

type CalibrationResult struct {
	Model          string `json:"model"`
	BatchSize      int    `json:"batch_size"`
	InputMessages  int    `json:"input_messages"`
	InputChars     int    `json:"input_chars"`
	PromptChars    int    `json:"prompt_chars"`
	DurationMillis int64  `json:"duration_ms"`
	EvalTokens     int    `json:"eval_tokens"`
	PromptTokens   int    `json:"prompt_tokens"`
	JSONValid      bool   `json:"json_valid"`
	Kept           int    `json:"kept"`
	Important      int    `json:"important"`
	Events         int    `json:"events"`
	Error          string `json:"error,omitempty"`
}

type EventInput struct {
	ID         string `json:"id"`
	SourceRef  string `json:"source_ref"`
	SourceLink string `json:"source_link,omitempty"`
	Text       string `json:"text"`
}

type EventCandidate struct {
	ID          string   `json:"id"`
	HasEvent    bool     `json:"has_event"`
	Title       string   `json:"title"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Location    string   `json:"location"`
	Description string   `json:"description"`
	Confidence  string   `json:"confidence"`
	Missing     []string `json:"missing"`
}

type EventExtractionResult struct {
	Events []EventCandidate `json:"events"`
}
