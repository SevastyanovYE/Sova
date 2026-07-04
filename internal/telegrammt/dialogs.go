package telegrammt

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	dialogquery "github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

const defaultDialogSearchLimit = 500

type ForumTopicsByTitle struct {
	RequestedTitle string
	Source         sqlitestore.TelegramSource
	BotAPIChatID   int64
	Topics         []sqlitestore.WorkspaceTopic
}

func (c *Client) DiscoverForumTopicsByTitles(ctx context.Context, titles []string, topicLimit int) ([]ForumTopicsByTitle, error) {
	if err := c.validateRuntime(); err != nil {
		return nil, err
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("at least one dialog title is required")
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return nil, err
	}

	client := c.newTelegramClient()
	var results []ForumTopicsByTitle
	err := client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("telegram session is not authorized; run `sova telegram-login`")
		}
		resolved, err := findDialogsByTitle(runCtx, client.API(), titles, defaultDialogSearchLimit)
		if err != nil {
			return err
		}
		results = make([]ForumTopicsByTitle, 0, len(resolved))
		for _, item := range resolved {
			topics, err := fetchForumTopics(runCtx, client.API(), item.source, topicLimit)
			if err != nil {
				return fmt.Errorf("fetch forum topics for %s: %w", item.source.source.Title, err)
			}
			results = append(results, ForumTopicsByTitle{
				RequestedTitle: item.requestedTitle,
				Source:         item.source.source,
				BotAPIChatID:   BotAPIChatID(item.source.source),
				Topics:         topics,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func BotAPIChatID(source sqlitestore.TelegramSource) int64 {
	switch source.PeerKind {
	case "channel":
		if source.ChatID < 0 {
			return source.ChatID
		}
		id, err := strconv.ParseInt("-100"+strconv.FormatInt(source.ChatID, 10), 10, 64)
		if err == nil {
			return id
		}
		return -source.ChatID
	case "chat":
		if source.ChatID < 0 {
			return source.ChatID
		}
		return -source.ChatID
	default:
		return source.ChatID
	}
}

type resolvedDialogTitle struct {
	requestedTitle string
	source         resolvedSource
}

func findDialogsByTitle(ctx context.Context, api *tg.Client, titles []string, limit int) ([]resolvedDialogTitle, error) {
	if limit <= 0 {
		limit = defaultDialogSearchLimit
	}
	wanted := map[string]string{}
	order := make([]string, 0, len(titles))
	for _, title := range titles {
		trimmed := strings.TrimSpace(title)
		if trimmed == "" {
			return nil, fmt.Errorf("dialog title cannot be empty")
		}
		key := normalizeDialogTitle(trimmed)
		if _, exists := wanted[key]; !exists {
			order = append(order, key)
		}
		wanted[key] = trimmed
	}
	found := map[string]resolvedDialogTitle{}
	iter := dialogquery.NewQueryBuilder(api).GetDialogs().BatchSize(100).Iter()
	scanned := 0
	for iter.Next(ctx) {
		if scanned >= limit || len(found) == len(wanted) {
			break
		}
		scanned++
		source, inputPeer, ok, err := sourceFromDialogElem(iter.Value())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		key := normalizeDialogTitle(source.Title)
		requested, want := wanted[key]
		if !want {
			continue
		}
		found[key] = resolvedDialogTitle{
			requestedTitle: requested,
			source: resolvedSource{
				configuredRef: requested,
				source:        source,
				inputPeer:     inputPeer,
			},
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}

	results := make([]resolvedDialogTitle, 0, len(order))
	var missing []string
	for _, key := range order {
		item, ok := found[key]
		if !ok {
			missing = append(missing, wanted[key])
			continue
		}
		results = append(results, item)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("dialog title(s) not found in first %d dialogs: %s", limit, strings.Join(missing, ", "))
	}
	return results, nil
}

func sourceFromDialogElem(elem dialogquery.Elem) (sqlitestore.TelegramSource, tg.InputPeerClass, bool, error) {
	switch peer := elem.Dialog.GetPeer().(type) {
	case *tg.PeerChat:
		chat, ok := elem.Entities.Chat(peer.ChatID)
		if !ok {
			return sqlitestore.TelegramSource{}, nil, false, fmt.Errorf("dialog chat %d missing from entities", peer.ChatID)
		}
		source, inputPeer, ok, err := sourceFromChatClass(chat)
		return source, inputPeer, ok, err
	case *tg.PeerChannel:
		channel, ok := elem.Entities.Channel(peer.ChannelID)
		if !ok {
			return sqlitestore.TelegramSource{}, nil, false, fmt.Errorf("dialog channel %d missing from entities", peer.ChannelID)
		}
		source, inputPeer, ok, err := sourceFromChatClass(channel)
		return source, inputPeer, ok, err
	default:
		return sqlitestore.TelegramSource{}, nil, false, nil
	}
}

func normalizeDialogTitle(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
