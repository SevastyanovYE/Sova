package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchDocumentEmbeddingLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	document := SearchDocument{
		Scope: "workspace", SourceRef: "telegram:channel:1", SourceTitle: "InSync",
		ChatID: -1001, MessageID: 42, TopicID: 7, MessageDate: now.Add(-time.Hour),
		Text: "Первая версия", MediaType: "message", SourceLink: "https://t.me/c/1/42", ContentHash: "hash-one",
	}
	changed, err := store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now)
	if err != nil || changed != 1 {
		t.Fatalf("upsert changed=%d err=%v", changed, err)
	}
	pending, err := store.PendingSearchDocuments(ctx, now, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	vector := make([]byte, 3*4)
	if err := store.SaveSearchEmbedding(ctx, pending[0].ID, "gemini-embedding-2", 3, vector, "primary", now); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ReadySearchEmbeddings(ctx, "gemini-embedding-2", 3)
	if err != nil || len(ready) != 1 || ready[0].ProviderRoute != "primary" {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}

	changed, err = store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now.Add(time.Minute))
	if err != nil || changed != 0 {
		t.Fatalf("unchanged upsert changed=%d err=%v", changed, err)
	}
	document.Text = "Исправленная версия"
	document.ContentHash = "hash-two"
	changed, err = store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now.Add(2*time.Minute))
	if err != nil || changed != 1 {
		t.Fatalf("edited upsert changed=%d err=%v", changed, err)
	}
	ready, err = store.ReadySearchEmbeddings(ctx, "gemini-embedding-2", 3)
	if err != nil || len(ready) != 0 {
		t.Fatalf("stale embeddings=%+v err=%v", ready, err)
	}
}

func TestSearchDocumentRetryWaitsUntilDue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "search-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	document := SearchDocument{Scope: "nest", ChatID: -1002, MessageID: 5, MessageDate: now, Text: "digest", ContentHash: "digest-hash"}
	if _, err := store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSearchDocuments(ctx, now, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := store.MarkSearchDocumentRetry(ctx, pending[0].ID, now.Add(time.Hour), "api failed", false, now); err != nil {
		t.Fatal(err)
	}
	before, err := store.PendingSearchDocuments(ctx, now.Add(59*time.Minute), 10)
	if err != nil || len(before) != 0 {
		t.Fatalf("before retry=%+v err=%v", before, err)
	}
	after, err := store.PendingSearchDocuments(ctx, now.Add(time.Hour), 10)
	if err != nil || len(after) != 1 || after[0].Attempts != 1 {
		t.Fatalf("after retry=%+v err=%v", after, err)
	}
}

func TestSearchReconcileDeletesAndRestoresDocument(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "search-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	document := SearchDocument{Scope: "legacy", ChatID: 3, MessageID: 9, MessageDate: now, Text: "quote", ContentHash: "stable"}
	if _, err := store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.PendingSearchDocuments(ctx, now, 10)
	if err := store.SaveSearchEmbedding(ctx, pending[0].ID, "gemini-embedding-2", 1, make([]byte, 4), "primary", now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileSearchScopeRange(ctx, "legacy", 8, 10, nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ReadySearchEmbeddings(ctx, "gemini-embedding-2", 1)
	if err != nil || len(ready) != 0 {
		t.Fatalf("ready after delete=%+v err=%v", ready, err)
	}
	if _, err := store.UpsertSearchDocuments(ctx, []SearchDocument{document}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingSearchDocuments(ctx, now.Add(2*time.Minute), 10)
	if err != nil || len(pending) != 1 || pending[0].Status != "active" {
		t.Fatalf("restored pending=%+v err=%v", pending, err)
	}
}

func TestOpenMigratesTelegramTopicIDForExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE telegram_sources (
 id INTEGER PRIMARY KEY AUTOINCREMENT, ref TEXT NOT NULL UNIQUE,
 peer_kind TEXT NOT NULL, chat_id INTEGER NOT NULL, access_hash INTEGER NOT NULL DEFAULT 0,
 title TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
 last_message_id INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL
);
CREATE TABLE telegram_messages (
 source_id INTEGER NOT NULL, chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
 date TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL DEFAULT '',
 media_type TEXT NOT NULL DEFAULT '', source_link TEXT NOT NULL DEFAULT '',
 raw_json TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(chat_id, message_id)
);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query("PRAGMA table_info(telegram_messages)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, column := range []string{"topic_id", "sender"} {
		if !found[column] {
			t.Fatalf("telegram_messages.%s was not migrated", column)
		}
	}
}

func TestSearchFullScanCheckpointResumesAndReconciles(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sova.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertSearchDocuments(ctx, []SearchDocument{
		{Scope: "workspace", SourceRef: "source", SourceTitle: "InSync", ChatID: -1001, MessageID: 1, MessageDate: now.Add(-time.Hour), Text: "keep", ContentHash: "h1"},
		{Scope: "workspace", SourceRef: "source", SourceTitle: "InSync", ChatID: -1001, MessageID: 2, MessageDate: now.Add(-time.Minute), Text: "deleted", ContentHash: "h2"},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	generation, cursor, err := store.BeginOrResumeSearchFullScan(ctx, "workspace", now)
	if err != nil || generation != 1 || cursor != 0 {
		t.Fatalf("begin generation=%d cursor=%d err=%v", generation, cursor, err)
	}
	if err := store.MarkSearchScanSeen(ctx, "workspace", generation, []int{1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckpointSearchFullScan(ctx, "workspace", generation, 1, 2, "interrupted", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	resumedGeneration, resumedCursor, err := store.BeginOrResumeSearchFullScan(ctx, "workspace", now.Add(2*time.Minute))
	if err != nil || resumedGeneration != generation || resumedCursor != 1 {
		t.Fatalf("resume generation=%d cursor=%d err=%v", resumedGeneration, resumedCursor, err)
	}
	if err := store.CompleteSearchFullScan(ctx, "workspace", generation, 2, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT message_id, status FROM search_documents WHERE scope = 'workspace'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	statuses := map[int]string{}
	for rows.Next() {
		var messageID int
		var status string
		if err := rows.Scan(&messageID, &status); err != nil {
			t.Fatal(err)
		}
		statuses[messageID] = status
	}
	if statuses[1] != "active" || statuses[2] != "deleted" {
		t.Fatalf("statuses=%v", statuses)
	}
}
