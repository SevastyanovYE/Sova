package model

import (
	"context"
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
	ID              string    `json:"id"`
	SourceRef       string    `json:"-"`
	Time            time.Time `json:"-"`
	Sender          string    `json:"-"`
	Kind            string    `json:"kind"`
	Text            string    `json:"text"`
	ExtractedText   string    `json:"extracted_text,omitempty"`
	AttachmentCount int       `json:"attachment_count,omitempty"`
}

type MessageDecision struct {
	ID         string   `json:"id"`
	Keep       bool     `json:"keep"`
	Importance int      `json:"importance"`
	Reason     string   `json:"reason,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	HasEvent   bool     `json:"has_event"`
}

type BatchResult struct {
	Decisions []MessageDecision `json:"decisions"`
}

type EventContext struct {
	Time time.Time
	Kind string
	Text string
}

type EventInput struct {
	ID         string `json:"id"`
	SourceRef  string `json:"-"`
	SourceLink string `json:"-"`
	Time       time.Time
	Kind       string
	Text       string `json:"text"`
	Context    []EventContext
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

type GenerateRequest struct {
	SystemPrompt    string
	UserPrompt      string
	ResponseSchema  map[string]any
	Temperature     float64
	MaxOutputTokens int
	ThinkingLevel   string
}

type GenerateResponse struct {
	Text         string
	Model        string
	FinishReason string
	PromptTokens int
	OutputTokens int
	TotalTokens  int
}

type Provider interface {
	Name() string
	Generate(context.Context, string, GenerateRequest) (GenerateResponse, error)
}

type Attempt struct {
	Provider      string
	Model         string
	Attempt       int
	BatchID       string
	InputMessages int
	InputChars    int
	Duration      time.Duration
	Success       bool
	ErrorClass    string
	StatusCode    int
	Error         string
	PromptTokens  int
	OutputTokens  int
	TotalTokens   int
	FinishReason  string
}

type Winner struct {
	Provider string
	Model    string
	Fallback bool
}

type ClassifyOutput struct {
	Decisions []MessageDecision
	Winners   map[string]Winner
	Attempts  []Attempt
	Fallbacks int
}

type EventOutput struct {
	Events   []EventCandidate
	Winners  map[string]Winner
	Attempts []Attempt
}
