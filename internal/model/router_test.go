package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/googleai"
)

type fakeProvider struct {
	calls []string
	fn    func(string, GenerateRequest) (GenerateResponse, error)
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Generate(_ context.Context, modelName string, request GenerateRequest) (GenerateResponse, error) {
	p.calls = append(p.calls, modelName)
	return p.fn(modelName, request)
}

func TestRouterFallsBackInOrderAndDisablesMissingModel(t *testing.T) {
	provider := &fakeProvider{fn: func(modelName string, _ GenerateRequest) (GenerateResponse, error) {
		if modelName == "missing" {
			return GenerateResponse{}, &googleai.APIError{StatusCode: 404, Model: modelName}
		}
		return GenerateResponse{Text: `{"decisions":[{"id":"m1","keep":true,"importance":2,"has_event":false}]}`}, nil
	}}
	router := NewRouter(provider, []string{"missing", "good"}, RouterOptions{})
	for i := 0; i < 2; i++ {
		output, err := router.Classify(context.Background(), fmt.Sprintf("b%d", i), []MessageInput{{ID: "m1", Text: "ДЗ"}})
		if err != nil || len(output.Decisions) != 1 || output.Winners["m1"].Model != "good" {
			t.Fatalf("output=%+v err=%v", output, err)
		}
	}
	want := []string{"missing", "good", "good"}
	if strings.Join(provider.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", provider.calls, want)
	}
}

func TestRouterSplitsInvalidBatchAndUsesKeepAllAtMinimum(t *testing.T) {
	provider := &fakeProvider{fn: func(_ string, _ GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{Text: `{"decisions":[]}`}, nil
	}}
	router := NewRouter(provider, []string{"one", "two"}, RouterOptions{ClassifyMinBatch: 4})
	inputs := make([]MessageInput, 8)
	for i := range inputs {
		inputs[i] = MessageInput{ID: fmt.Sprintf("m%d", i), Text: "text"}
	}
	output, err := router.Classify(context.Background(), "b1", inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Decisions) != 8 || output.Fallbacks != 8 {
		t.Fatalf("output = %+v", output)
	}
	for _, decision := range output.Decisions {
		if !decision.Keep || decision.Importance != 1 || decision.HasEvent {
			t.Fatalf("decision = %+v", decision)
		}
	}
}

func TestRouterDoesNotSplitClassificationBelowFourMessageMinimum(t *testing.T) {
	provider := &fakeProvider{fn: func(_ string, _ GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{Text: `{"decisions":[]}`}, nil
	}}
	router := NewRouter(provider, []string{"one", "two"}, RouterOptions{ClassifyMinBatch: 4})
	inputs := make([]MessageInput, 5)
	for i := range inputs {
		inputs[i] = MessageInput{ID: fmt.Sprintf("m%d", i), Text: "text"}
	}
	output, err := router.Classify(context.Background(), "b1", inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 2 || output.Fallbacks != 5 {
		t.Fatalf("calls=%v output=%+v", provider.calls, output)
	}
}

func TestEventRouterRequiresOneResultPerInput(t *testing.T) {
	provider := &fakeProvider{fn: func(_ string, _ GenerateRequest) (GenerateResponse, error) {
		return GenerateResponse{Text: `{"events":[{"id":"e1","has_event":false,"title":"","start":"","end":"","location":"","description":"","confidence":"low","missing":[]}]}`}, nil
	}}
	router := NewRouter(provider, []string{"good"}, RouterOptions{})
	output, err := router.ExtractEvents(context.Background(), "e", []EventInput{{ID: "e1", Text: "завтра"}}, time.Now(), "Europe/Moscow")
	if err != nil || len(output.Events) != 1 {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}

func TestPromptsDoNotContainSourceRefsOrLinks(t *testing.T) {
	prompt, err := BuildClassificationPrompt([]MessageInput{{ID: "m1", SourceRef: "telegram:100:42", Text: "ДЗ", Sender: "Группа"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "telegram:100:42") {
		t.Fatalf("classification prompt leaked source ref: %s", prompt)
	}
	eventPrompt, err := BuildEventPrompt([]EventInput{{ID: "e1", SourceRef: "telegram:100:42", SourceLink: "https://t.me/c/100/42", Text: "Экзамен завтра"}}, time.Now(), "Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eventPrompt, "telegram:100:42") || strings.Contains(eventPrompt, "t.me") {
		t.Fatalf("event prompt leaked identity: %s", eventPrompt)
	}
}
