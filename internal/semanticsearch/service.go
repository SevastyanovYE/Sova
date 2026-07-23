package semanticsearch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/googleai"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

const (
	DefaultEmbeddingModel = "gemini-embedding-2"
	defaultResultLimit    = 10
	// The observed free-tier item window is 100 embeddings per project.
	// Twenty keeps requests compact and fills that window without stranding a
	// partially over-limit batch before switching to the fallback project.
	embeddingBatchSize = 20
)

var ErrIndexNotReady = errors.New("semantic search index has not completed its full scan")

type embedClient interface {
	EmbedContent(context.Context, googleai.EmbedRequest) (googleai.EmbedResponse, error)
	BatchEmbedContents(context.Context, googleai.BatchEmbedRequest) (googleai.BatchEmbedResponse, error)
}

type Service struct {
	store       *sqlitestore.Store
	model       string
	primary     embedClient
	fallback    embedClient
	primaryKey  string
	fallbackKey string
	now         func() time.Time
}

type SearchResult struct {
	Document sqlitestore.SearchDocument
	Score    float64
}

type IndexSummary struct {
	Processed int
	Primary   int
	Fallback  int
	Queued    int
}

func NewService(store *sqlitestore.Store, model, primaryKey, fallbackKey string) *Service {
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultEmbeddingModel
	}
	service := &Service{
		store: store, model: model, primaryKey: strings.TrimSpace(primaryKey),
		fallbackKey: strings.TrimSpace(fallbackKey), now: time.Now,
	}
	service.primary = googleai.New(service.primaryKey)
	if service.fallbackKey != "" {
		service.fallback = googleai.New(service.fallbackKey)
	}
	return service
}

func (s *Service) UpsertCorpus(ctx context.Context, scope string, messages []sqlitestore.TelegramRecentMessage) (int, []int, error) {
	if s == nil || s.store == nil {
		return 0, nil, fmt.Errorf("search store is required")
	}
	documents := make([]sqlitestore.SearchDocument, 0, len(messages))
	seen := make([]int, 0, len(messages))
	for _, message := range messages {
		text := strings.TrimSpace(message.Text)
		if text == "" || message.Kind == "service" || isSearchCommand(text) {
			continue
		}
		seen = append(seen, message.MessageID)
		documents = append(documents, sqlitestore.SearchDocument{
			Scope: scope, SourceRef: message.SourceRef, SourceTitle: message.SourceTitle,
			ChatID: message.ChatID, MessageID: message.MessageID, TopicID: message.TopicID,
			MessageDate: message.Date, Text: text, MediaType: message.MediaType,
			SourceLink: message.SourceLink, ContentHash: ContentHash(scope, message.ChatID, message.MessageID, text),
		})
	}
	changed, err := s.store.UpsertSearchDocuments(ctx, documents, s.now().UTC())
	return changed, seen, err
}

func (s *Service) IndexPending(ctx context.Context, limit int) (IndexSummary, error) {
	if s == nil || s.store == nil {
		return IndexSummary{}, fmt.Errorf("search store is required")
	}
	documents, err := s.store.PendingSearchDocuments(ctx, s.now().UTC(), limit)
	if err != nil {
		return IndexSummary{}, err
	}
	var summary IndexSummary
	for start := 0; start < len(documents); start += embeddingBatchSize {
		end := start + embeddingBatchSize
		if end > len(documents) {
			end = len(documents)
		}
		batch := documents[start:end]
		texts := make([]string, len(batch))
		for index, document := range batch {
			texts[index] = prepareDocument(document.SourceTitle, document.Text)
		}
		vectors, route, embedErr := s.embedBatch(ctx, texts)
		if embedErr != nil {
			now := s.now().UTC()
			for _, document := range batch {
				delay := searchRetryDelay(document.Attempts + 1)
				if err := s.store.MarkSearchDocumentRetry(ctx, document.ID, now.Add(delay), redactSearchError(embedErr.Error(), s.primaryKey, s.fallbackKey), false, now); err != nil {
					return summary, err
				}
				summary.Queued++
			}
			return summary, fmt.Errorf("documents %d-%d: embedding temporarily unavailable", batch[0].ID, batch[len(batch)-1].ID)
		}
		for index, document := range batch {
			normalized, err := Normalize(vectors[index])
			if err != nil {
				return summary, err
			}
			if err := s.store.SaveSearchEmbedding(ctx, document.ID, s.model, DefaultEmbeddingDimensions, EncodeFloat32(normalized), route, s.now().UTC()); err != nil {
				return summary, err
			}
			summary.Processed++
			if route == "fallback" {
				summary.Fallback++
			} else {
				summary.Primary++
			}
		}
	}
	return summary, nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]SearchResult, string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", fmt.Errorf("search query is empty")
	}
	ready, err := s.IndexReady(ctx)
	if err != nil {
		return nil, "", err
	}
	if !ready {
		return nil, "", ErrIndexNotReady
	}
	vector, route, err := s.embed(ctx, "task: search result | query: "+query)
	if err != nil {
		return nil, "", fmt.Errorf("semantic search is temporarily unavailable: %w", err)
	}
	normalized, err := Normalize(vector)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.store.ReadySearchEmbeddings(ctx, s.model, DefaultEmbeddingDimensions)
	if err != nil {
		return nil, "", err
	}
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		stored, err := DecodeFloat32(row.Vector, row.Dimensions)
		if err != nil {
			continue
		}
		score, err := Cosine(normalized, stored)
		if err != nil {
			continue
		}
		results = append(results, SearchResult{Document: row.Document, Score: score})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Document.MessageDate.After(results[j].Document.MessageDate)
		}
		return results[i].Score > results[j].Score
	})
	if limit <= 0 || limit > defaultResultLimit {
		limit = defaultResultLimit
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, route, nil
}

func (s *Service) IndexReady(ctx context.Context) (bool, error) {
	states, err := s.store.SearchSyncStates(ctx)
	if err != nil {
		return false, err
	}
	ready := map[string]bool{"workspace": false, "legacy": false, "nest": false}
	for _, state := range states {
		if state.FullScanCompletedAt != nil && state.LastError == "" {
			ready[state.Scope] = true
		}
	}
	if !ready["workspace"] || !ready["legacy"] || !ready["nest"] {
		return false, nil
	}
	pending, err := s.store.SearchIndexMissingEmbeddingCount(ctx, s.model, DefaultEmbeddingDimensions)
	if err != nil {
		return false, err
	}
	return pending == 0, nil
}

func (s *Service) embed(ctx context.Context, text string) ([]float32, string, error) {
	request := googleai.EmbedRequest{Model: s.model, Text: text, OutputDimensions: DefaultEmbeddingDimensions}
	response, primaryErr := s.primary.EmbedContent(ctx, request)
	if primaryErr == nil {
		return response.Values, "primary", nil
	}
	if !searchFallbackEligible(primaryErr) || s.fallback == nil {
		return nil, "", fmt.Errorf("primary embedding route failed: %s", redactSearchError(primaryErr.Error(), s.primaryKey, s.fallbackKey))
	}
	response, fallbackErr := s.fallback.EmbedContent(ctx, request)
	if fallbackErr == nil {
		return response.Values, "fallback", nil
	}
	return nil, "", fmt.Errorf("primary and fallback embedding routes failed: %s; %s",
		redactSearchError(primaryErr.Error(), s.primaryKey, s.fallbackKey),
		redactSearchError(fallbackErr.Error(), s.primaryKey, s.fallbackKey))
}

func (s *Service) embedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	request := googleai.BatchEmbedRequest{Model: s.model, Texts: texts, OutputDimensions: DefaultEmbeddingDimensions}
	response, primaryErr := s.primary.BatchEmbedContents(ctx, request)
	if primaryErr == nil {
		return response.Values, "primary", nil
	}
	if !searchFallbackEligible(primaryErr) || s.fallback == nil {
		return nil, "", fmt.Errorf("primary embedding route failed: %s", redactSearchError(primaryErr.Error(), s.primaryKey, s.fallbackKey))
	}
	response, fallbackErr := s.fallback.BatchEmbedContents(ctx, request)
	if fallbackErr == nil {
		return response.Values, "fallback", nil
	}
	return nil, "", fmt.Errorf("primary and fallback embedding routes failed: %s; %s",
		redactSearchError(primaryErr.Error(), s.primaryKey, s.fallbackKey),
		redactSearchError(fallbackErr.Error(), s.primaryKey, s.fallbackKey))
}

func prepareDocument(title, text string) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + strings.TrimSpace(text)
}

func isSearchCommand(text string) bool {
	first := strings.ToLower(strings.Fields(text)[0])
	return first == "/search" || strings.HasPrefix(first, "/search@")
}

func searchFallbackEligible(err error) bool {
	class, _, _, tryNext := googleai.ClassifyError(err)
	return tryNext || class == googleai.ErrorAuthentication || class == googleai.ErrorModelUnavailable
}

func searchRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	case 4:
		return time.Hour
	default:
		return time.Hour
	}
}

func redactSearchError(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 300 {
		value = string([]rune(value)[:299]) + "…"
	}
	return value
}
