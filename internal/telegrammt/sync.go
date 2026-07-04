package telegrammt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/gotd/td/telegram/deeplink"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

const (
	defaultSyncLimit     = 100
	maxHistoryBatchLimit = 100
	recentIndexLimit     = 80
)

type SyncOptions struct {
	LimitPerSource int
	DryRun         bool
	Backfill       bool
	FullScan       bool
}

type SyncSourceResult struct {
	ConfiguredRef string
	SourceRef     string
	Title         string
	Username      string
	Fetched       int
	New           int
	Inserted      int
	Messages      []SyncedMessage
}

type SyncResult struct {
	Sources []SyncSourceResult
}

type SyncedMessage struct {
	SourceRef   string
	SourceTitle string
	Username    string
	ChatID      int64
	MessageID   int
	Date        time.Time
	Kind        string
	Text        string
	MediaType   string
	SourceLink  string
}

func (r SyncResult) NewMessages() []SyncedMessage {
	var messages []SyncedMessage
	for _, source := range r.Sources {
		messages = append(messages, source.Messages...)
	}
	return messages
}

func syncResultSourceRefs(result SyncResult) []string {
	seen := map[string]struct{}{}
	refs := make([]string, 0, len(result.Sources))
	for _, source := range result.Sources {
		ref := strings.TrimSpace(source.SourceRef)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

type resolvedSource struct {
	configuredRef string
	source        sqlitestore.TelegramSource
	inputPeer     tg.InputPeerClass
}

type rawMessageRecord struct {
	Schema        string      `json:"schema"`
	SourceRef     string      `json:"source_ref"`
	ConfiguredRef string      `json:"configured_ref"`
	ChatID        int64       `json:"chat_id"`
	MessageID     int         `json:"message_id"`
	Date          string      `json:"date"`
	Kind          string      `json:"kind"`
	Text          string      `json:"text,omitempty"`
	MediaType     string      `json:"media_type,omitempty"`
	SourceLink    string      `json:"source_link,omitempty"`
	TLType        string      `json:"tl_type"`
	TL            interface{} `json:"tl"`
}

func (c *Client) Sync(ctx context.Context, store *sqlitestore.Store, opts SyncOptions) (SyncResult, error) {
	if len(c.cfg.NestTelegramAllowedChats) == 0 {
		return SyncResult{}, fmt.Errorf("SOVA_NEST_TELEGRAM_ALLOWED_CHATS must contain at least one Sova Nest study source")
	}
	return c.syncSources(ctx, store, c.cfg.NestTelegramAllowedChats, opts, true)
}

func (c *Client) SyncWorkspaceLegacy(ctx context.Context, store *sqlitestore.Store, opts SyncOptions) (SyncResult, error) {
	source := strings.TrimSpace(c.cfg.Workspace.LegacySource)
	if source == "" {
		return SyncResult{}, fmt.Errorf("SOVA_WORKSPACE_LEGACY_SOURCE must contain the old InSync source")
	}
	return c.syncSources(ctx, store, []string{source}, opts, false)
}

func (c *Client) syncSources(ctx context.Context, store *sqlitestore.Store, configuredRefs []string, opts SyncOptions, writeRecentIndex bool) (SyncResult, error) {
	if store == nil {
		return SyncResult{}, fmt.Errorf("store is required")
	}
	if err := c.validateRuntime(); err != nil {
		return SyncResult{}, err
	}
	if len(configuredRefs) == 0 {
		return SyncResult{}, fmt.Errorf("at least one Telegram source is required")
	}
	if opts.LimitPerSource <= 0 {
		opts.LimitPerSource = defaultSyncLimit
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return SyncResult{}, err
	}

	client := c.newTelegramClient()
	var result SyncResult
	err := client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("telegram session is not authorized; run `sova telegram-login`")
		}

		resolver := peers.Options{}.Build(client.API())
		for _, configuredRef := range configuredRefs {
			source, err := c.resolveSyncSource(runCtx, store, client.API(), resolver, configuredRef)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", configuredRef, err)
			}

			storedSource := source.source
			if !opts.DryRun {
				storedSource, err = store.UpsertTelegramSource(runCtx, source.source, time.Now().UTC())
				if err != nil {
					return fmt.Errorf("upsert %s: %w", source.source.Ref, err)
				}
			} else if existing, ok := existingSource(runCtx, store, source.source.Ref); ok {
				storedSource = existing
			}

			minID := storedSource.LastMessageID
			maxID := 0
			if opts.Backfill && storedSource.ID != 0 {
				oldestMessageID, _, ok, err := store.TelegramMessageIDBounds(runCtx, storedSource.ID)
				if err != nil {
					return fmt.Errorf("read message bounds for %s: %w", source.source.Ref, err)
				}
				if ok {
					maxID = oldestMessageID
				}
			}
			if opts.Backfill || opts.FullScan {
				minID = 0
			}

			messages, err := c.fetchSourceMessages(runCtx, client.API(), source, opts.LimitPerSource, minID, maxID)
			if err != nil {
				return fmt.Errorf("fetch %s: %w", source.source.Ref, err)
			}
			for i := range messages {
				messages[i].SourceID = storedSource.ID
			}
			newMessages, err := store.FilterNewTelegramMessages(runCtx, messages)
			if err != nil {
				return fmt.Errorf("filter new messages for %s: %w", source.source.Ref, err)
			}
			if !opts.DryRun {
				if err := appendTelegramRawJSONL(c.cfg, newMessages); err != nil {
					return fmt.Errorf("append raw telegram messages: %w", err)
				}
			}

			inserted := 0
			if !opts.DryRun {
				inserted, _, err = store.InsertTelegramMessages(runCtx, newMessages)
				if err != nil {
					return fmt.Errorf("insert telegram messages for %s: %w", source.source.Ref, err)
				}
			}
			result.Sources = append(result.Sources, SyncSourceResult{
				ConfiguredRef: configuredRef,
				SourceRef:     source.source.Ref,
				Title:         source.source.Title,
				Username:      source.source.Username,
				Fetched:       len(messages),
				New:           len(newMessages),
				Inserted:      inserted,
				Messages:      compactSyncedMessages(source.source, newMessages),
			})
		}

		if !opts.DryRun && writeRecentIndex {
			recent, err := store.RecentTelegramMessagesBySourceRefs(runCtx, syncResultSourceRefs(result), recentIndexLimit)
			if err != nil {
				return fmt.Errorf("load recent telegram messages: %w", err)
			}
			if err := WriteTelegramRecentIndex(c.cfg, recent, time.Now().UTC()); err != nil {
				return fmt.Errorf("write telegram recent index: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

func compactSyncedMessages(source sqlitestore.TelegramSource, messages []sqlitestore.TelegramMessage) []SyncedMessage {
	out := make([]SyncedMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, SyncedMessage{
			SourceRef:   source.Ref,
			SourceTitle: source.Title,
			Username:    source.Username,
			ChatID:      message.ChatID,
			MessageID:   message.MessageID,
			Date:        message.Date,
			Kind:        message.Kind,
			Text:        message.Text,
			MediaType:   message.MediaType,
			SourceLink:  message.SourceLink,
		})
	}
	return out
}

func existingSource(ctx context.Context, store *sqlitestore.Store, ref string) (sqlitestore.TelegramSource, bool) {
	source, err := store.TelegramSourceByRef(ctx, ref)
	return source, err == nil
}

func (c *Client) resolveSyncSource(ctx context.Context, store *sqlitestore.Store, api *tg.Client, resolver *peers.Manager, configuredRef string) (resolvedSource, error) {
	configuredRef = strings.TrimSpace(configuredRef)
	if configuredRef == "" {
		return resolvedSource{}, fmt.Errorf("empty source ref")
	}
	if source, inputPeer, ok, err := resolveExplicitSource(ctx, store, configuredRef); ok || err != nil {
		if err != nil {
			return resolvedSource{}, err
		}
		return resolvedSource{configuredRef: configuredRef, source: source, inputPeer: inputPeer}, nil
	}
	if source, inputPeer, ok, err := resolveJoinedInvite(ctx, api, configuredRef); ok || err != nil {
		if err != nil {
			return resolvedSource{}, err
		}
		return resolvedSource{configuredRef: configuredRef, source: source, inputPeer: inputPeer}, nil
	}

	peer, err := resolver.Resolve(ctx, configuredRef)
	if err != nil {
		return resolvedSource{}, err
	}
	inputPeer := peer.InputPeer()
	peerKind, chatID, accessHash, err := inputPeerIdentity(inputPeer)
	if err != nil {
		return resolvedSource{}, err
	}
	username, _ := peer.Username()
	source := sqlitestore.TelegramSource{
		Ref:        stableSourceRef(peerKind, chatID),
		PeerKind:   peerKind,
		ChatID:     chatID,
		AccessHash: accessHash,
		Title:      peer.VisibleName(),
		Username:   username,
	}
	return resolvedSource{configuredRef: configuredRef, source: source, inputPeer: inputPeer}, nil
}

func resolveJoinedInvite(ctx context.Context, api *tg.Client, ref string) (sqlitestore.TelegramSource, tg.InputPeerClass, bool, error) {
	link, err := deeplink.Parse(ref)
	if err != nil || link.Type != deeplink.Join {
		return sqlitestore.TelegramSource{}, nil, false, nil
	}
	invite, err := api.MessagesCheckChatInvite(ctx, link.Args.Get("invite"))
	if err != nil {
		return sqlitestore.TelegramSource{}, nil, true, err
	}
	already, ok := invite.(*tg.ChatInviteAlready)
	if !ok {
		return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("invite points to a chat that this Telegram account has not joined yet")
	}
	return sourceFromChatClass(already.GetChat())
}

func sourceFromChatClass(chat tg.ChatClass) (sqlitestore.TelegramSource, tg.InputPeerClass, bool, error) {
	switch typed := chat.(type) {
	case *tg.Chat:
		source := sqlitestore.TelegramSource{
			Ref:      stableSourceRef("chat", typed.ID),
			PeerKind: "chat",
			ChatID:   typed.ID,
			Title:    typed.Title,
		}
		return source, typed.AsInputPeer(), true, nil
	case *tg.Channel:
		accessHash, _ := typed.GetAccessHash()
		username, _ := typed.GetUsername()
		source := sqlitestore.TelegramSource{
			Ref:        stableSourceRef("channel", typed.ID),
			PeerKind:   "channel",
			ChatID:     typed.ID,
			AccessHash: accessHash,
			Title:      typed.Title,
			Username:   username,
		}
		return source, typed.AsInputPeer(), true, nil
	default:
		return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("unsupported invite chat type %T", chat)
	}
}

func resolveExplicitSource(ctx context.Context, store *sqlitestore.Store, ref string) (sqlitestore.TelegramSource, tg.InputPeerClass, bool, error) {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, "-100") {
		channelID := strings.TrimPrefix(trimmed, "-100")
		if channelID == "" {
			return sqlitestore.TelegramSource{}, nil, false, nil
		}
		if _, err := strconv.ParseInt(channelID, 10, 64); err != nil {
			return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("parse channel id: %w", err)
		}
		trimmed = "channel:" + channelID
	} else {
		trimmed = strings.TrimPrefix(trimmed, "telegram:")
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) < 2 {
		return sqlitestore.TelegramSource{}, nil, false, nil
	}
	peerKind := parts[0]
	if peerKind != "chat" && peerKind != "channel" && peerKind != "user" {
		return sqlitestore.TelegramSource{}, nil, false, nil
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("parse %s id: %w", peerKind, err)
	}
	source := sqlitestore.TelegramSource{
		Ref:      stableSourceRef(peerKind, chatID),
		PeerKind: peerKind,
		ChatID:   chatID,
	}
	var accessHash int64
	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		accessHash, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("parse %s access hash: %w", peerKind, err)
		}
		source.AccessHash = accessHash
	} else if existing, ok := existingSource(ctx, store, source.Ref); ok {
		source = existing
		accessHash = existing.AccessHash
	}

	switch peerKind {
	case "chat":
		return source, &tg.InputPeerChat{ChatID: chatID}, true, nil
	case "channel":
		if accessHash == 0 {
			return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("channel refs require an access hash on first use: channel:%d:<access_hash>", chatID)
		}
		return source, &tg.InputPeerChannel{ChannelID: chatID, AccessHash: accessHash}, true, nil
	case "user":
		if accessHash == 0 {
			return sqlitestore.TelegramSource{}, nil, true, fmt.Errorf("user refs require an access hash on first use: user:%d:<access_hash>", chatID)
		}
		return source, &tg.InputPeerUser{UserID: chatID, AccessHash: accessHash}, true, nil
	default:
		return sqlitestore.TelegramSource{}, nil, false, nil
	}
}

func inputPeerIdentity(inputPeer tg.InputPeerClass) (string, int64, int64, error) {
	switch peer := inputPeer.(type) {
	case *tg.InputPeerChat:
		return "chat", peer.ChatID, 0, nil
	case *tg.InputPeerChannel:
		return "channel", peer.ChannelID, peer.AccessHash, nil
	case *tg.InputPeerUser:
		return "user", peer.UserID, peer.AccessHash, nil
	default:
		return "", 0, 0, fmt.Errorf("unsupported input peer type %T", inputPeer)
	}
}

func stableSourceRef(peerKind string, chatID int64) string {
	return "telegram:" + peerKind + ":" + strconv.FormatInt(chatID, 10)
}

func (c *Client) fetchSourceMessages(ctx context.Context, api *tg.Client, source resolvedSource, limit int, minID int, maxID int) ([]sqlitestore.TelegramMessage, error) {
	var messages []sqlitestore.TelegramMessage
	seen := map[int]struct{}{}
	offsetID := 0
	for len(messages) < limit {
		batchLimit := limit - len(messages)
		if batchLimit > maxHistoryBatchLimit {
			batchLimit = maxHistoryBatchLimit
		}
		request := &tg.MessagesGetHistoryRequest{
			Peer:     source.inputPeer,
			Limit:    batchLimit,
			OffsetID: offsetID,
		}
		if maxID > 0 {
			request.MaxID = maxID
		}
		if minID > 0 {
			request.MinID = minID
		}
		history, err := api.MessagesGetHistory(ctx, request)
		if err != nil {
			return nil, err
		}
		modified, ok := history.AsModified()
		if !ok {
			break
		}
		rawMessages := modified.GetMessages()
		if len(rawMessages) == 0 {
			break
		}
		oldestID := 0
		for _, raw := range rawMessages {
			if notEmpty, ok := raw.AsNotEmpty(); ok {
				messageID := notEmpty.GetID()
				if oldestID == 0 || messageID < oldestID {
					oldestID = messageID
				}
			}
			message, ok, err := c.convertTelegramMessage(source, raw)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if _, ok := seen[message.MessageID]; ok {
				continue
			}
			seen[message.MessageID] = struct{}{}
			messages = append(messages, message)
			if len(messages) >= limit {
				break
			}
		}
		if oldestID == 0 || oldestID == offsetID || len(rawMessages) < batchLimit {
			break
		}
		offsetID = oldestID
	}
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].Date.Equal(messages[j].Date) {
			return messages[i].MessageID < messages[j].MessageID
		}
		return messages[i].Date.Before(messages[j].Date)
	})
	return messages, nil
}

func (c *Client) convertTelegramMessage(source resolvedSource, raw tg.MessageClass) (sqlitestore.TelegramMessage, bool, error) {
	notEmpty, ok := raw.AsNotEmpty()
	if !ok {
		return sqlitestore.TelegramMessage{}, false, nil
	}
	messageID := notEmpty.GetID()
	if messageID == 0 {
		return sqlitestore.TelegramMessage{}, false, nil
	}
	date := time.Unix(int64(notEmpty.GetDate()), 0).UTC()
	kind := "message"
	text := ""
	mediaType := ""

	switch typed := raw.(type) {
	case *tg.Message:
		text = strings.TrimSpace(typed.GetMessage())
		if media, ok := typed.GetMedia(); ok && media != nil {
			mediaType = media.TypeName()
		}
	case *tg.MessageService:
		kind = "service"
		action := typed.GetAction()
		if action != nil {
			mediaType = action.TypeName()
		}
	default:
		kind = raw.TypeName()
	}

	record := rawMessageRecord{
		Schema:        "sova.telegram.raw-message.v1",
		SourceRef:     source.source.Ref,
		ConfiguredRef: source.configuredRef,
		ChatID:        source.source.ChatID,
		MessageID:     messageID,
		Date:          date.Format(time.RFC3339),
		Kind:          kind,
		Text:          text,
		MediaType:     mediaType,
		SourceLink:    sourceMessageLink(source.source, messageID),
		TLType:        raw.TypeName(),
		TL:            raw,
	}
	rawJSON, err := json.Marshal(record)
	if err != nil {
		return sqlitestore.TelegramMessage{}, false, fmt.Errorf("marshal raw message %d: %w", messageID, err)
	}

	return sqlitestore.TelegramMessage{
		ChatID:     source.source.ChatID,
		MessageID:  messageID,
		Date:       date,
		Kind:       kind,
		Text:       text,
		MediaType:  mediaType,
		SourceLink: record.SourceLink,
		RawJSON:    string(rawJSON),
	}, true, nil
}

func sourceMessageLink(source sqlitestore.TelegramSource, messageID int) string {
	if source.Username != "" {
		return "https://t.me/" + url.PathEscape(source.Username) + "/" + strconv.Itoa(messageID)
	}
	if source.PeerKind == "channel" && source.ChatID != 0 {
		return "https://t.me/c/" + strconv.FormatInt(source.ChatID, 10) + "/" + strconv.Itoa(messageID)
	}
	return ""
}

func appendTelegramRawJSONL(cfg config.Config, messages []sqlitestore.TelegramMessage) error {
	if len(messages) == 0 {
		return nil
	}
	path := filepath.Join(cfg.StateDir, "raw", "telegram", "messages.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, message := range messages {
		if strings.TrimSpace(message.RawJSON) == "" {
			return fmt.Errorf("message %d has empty raw JSON", message.MessageID)
		}
		if _, err := file.WriteString(message.RawJSON + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func WriteTelegramRecentIndex(cfg config.Config, messages []sqlitestore.TelegramRecentMessage, generatedAt time.Time) error {
	path := filepath.Join(cfg.StateDir, "index", "telegram-recent.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		location = time.UTC
	}

	var b strings.Builder
	b.WriteString("# Recent Telegram Content\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(location).Format(time.RFC3339))
	b.WriteString("\n\n")
	if len(messages) == 0 {
		b.WriteString("No Telegram messages indexed yet.\n")
		return os.WriteFile(path, []byte(b.String()), 0o600)
	}
	currentSource := ""
	for _, message := range messages {
		sourceLabel := message.SourceTitle
		if sourceLabel == "" {
			sourceLabel = message.SourceRef
		}
		if sourceLabel != currentSource {
			if currentSource != "" {
				b.WriteString("\n")
			}
			currentSource = sourceLabel
			b.WriteString("## ")
			b.WriteString(compactMarkdownLine(sourceLabel, 120))
			b.WriteString("\n\n")
		}
		b.WriteString("- `")
		b.WriteString(message.Date.In(location).Format("2006-01-02 15:04"))
		b.WriteString("` `")
		b.WriteString(message.Kind)
		b.WriteString("` ")
		if message.SourceLink != "" {
			b.WriteString("[#")
			b.WriteString(strconv.Itoa(message.MessageID))
			b.WriteString("](")
			b.WriteString(message.SourceLink)
			b.WriteString(")")
		} else {
			b.WriteString("#")
			b.WriteString(strconv.Itoa(message.MessageID))
		}
		body := message.Text
		if body == "" && message.MediaType != "" {
			body = "[" + message.MediaType + "]"
		}
		if body != "" {
			b.WriteString(": ")
			b.WriteString(compactMarkdownLine(body, 280))
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func compactMarkdownLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.ReplaceAll(value, "[", "\\[")
	value = strings.ReplaceAll(value, "]", "\\]")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
