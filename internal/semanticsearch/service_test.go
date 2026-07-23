package semanticsearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/googleai"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type fakeEmbedClient struct {
	values     []float32
	err        error
	batchErr   error
	calls      int
	batchCalls int
}

func (f *fakeEmbedClient) EmbedContent(context.Context, googleai.EmbedRequest) (googleai.EmbedResponse, error) {
	f.calls++
	return googleai.EmbedResponse{Values: f.values, Model: DefaultEmbeddingModel}, f.err
}

func (f *fakeEmbedClient) BatchEmbedContents(_ context.Context, request googleai.BatchEmbedRequest) (googleai.BatchEmbedResponse, error) {
	f.batchCalls++
	if f.batchErr != nil {
		return googleai.BatchEmbedResponse{}, f.batchErr
	}
	values := make([][]float32, len(request.Texts))
	for index := range values {
		values[index] = append([]float32(nil), f.values...)
	}
	return googleai.BatchEmbedResponse{Values: values, Model: DefaultEmbeddingModel}, nil
}

func TestServiceFallsBackToSecondKeyForAuthenticationError(t *testing.T) {
	primary := &fakeEmbedClient{err: &googleai.APIError{StatusCode: http.StatusForbidden}}
	fallback := &fakeEmbedClient{values: unitVector(0)}
	service := &Service{model: DefaultEmbeddingModel, primary: primary, fallback: fallback, now: time.Now}
	values, route, err := service.embed(context.Background(), "query")
	if err != nil || route != "fallback" || len(values) != DefaultEmbeddingDimensions {
		t.Fatalf("route=%q values=%d err=%v", route, len(values), err)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primary.calls, fallback.calls)
	}
}

func TestServiceExactCosineSearchRequiresFullScan(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "semantic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	queryVector := unitVector(0)
	client := &fakeEmbedClient{values: queryVector}
	service := &Service{store: store, model: DefaultEmbeddingModel, primary: client, now: func() time.Time { return now }}

	_, _, err = service.Search(ctx, "сова", 10)
	if !errors.Is(err, ErrIndexNotReady) {
		t.Fatalf("search before full scan error = %v", err)
	}
	for _, scope := range []string{"workspace", "legacy", "nest"} {
		if err := store.SetSearchSyncState(ctx, scope, 10, true, "", now); err != nil {
			t.Fatal(err)
		}
	}
	documents := []sqlitestore.SearchDocument{
		{Scope: "workspace", ChatID: 1, MessageID: 1, MessageDate: now, Text: "owl", ContentHash: "one"},
		{Scope: "nest", ChatID: 2, MessageID: 2, MessageDate: now.Add(-time.Hour), Text: "calendar", ContentHash: "two"},
	}
	if _, err := store.UpsertSearchDocuments(ctx, documents, now); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSearchDocuments(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range pending {
		vector := unitVector(1)
		if document.MessageID == 1 {
			vector = unitVector(0)
		}
		if err := store.SaveSearchEmbedding(ctx, document.ID, DefaultEmbeddingModel, DefaultEmbeddingDimensions, EncodeFloat32(vector), "primary", now); err != nil {
			t.Fatal(err)
		}
	}
	results, route, err := service.Search(ctx, "сова", 10)
	if err != nil || route != "primary" || len(results) != 2 || results[0].Document.MessageID != 1 {
		t.Fatalf("route=%q results=%+v err=%v", route, results, err)
	}
}

func TestIndexPendingUsesSmallSequentialBatches(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "semantic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	documents := make([]sqlitestore.SearchDocument, 41)
	for index := range documents {
		documents[index] = sqlitestore.SearchDocument{
			Scope: "legacy", ChatID: 1, MessageID: index + 1, MessageDate: now,
			Text: "message", ContentHash: fmt.Sprintf("hash-%d", index),
		}
	}
	if _, err := store.UpsertSearchDocuments(ctx, documents, now); err != nil {
		t.Fatal(err)
	}
	client := &fakeEmbedClient{values: unitVector(0)}
	service := &Service{store: store, model: DefaultEmbeddingModel, primary: client, now: func() time.Time { return now }}
	summary, err := service.IndexPending(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Processed != 41 || summary.Primary != 41 || client.batchCalls != 3 || client.calls != 0 {
		t.Fatalf("summary=%+v batch_calls=%d calls=%d", summary, client.batchCalls, client.calls)
	}
}

func unitVector(index int) []float32 {
	values := make([]float32, DefaultEmbeddingDimensions)
	values[index] = 1
	return values
}
