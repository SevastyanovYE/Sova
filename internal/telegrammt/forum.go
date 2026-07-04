package telegrammt

import (
	"context"
	"fmt"
	"time"

	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
)

const defaultForumTopicLimit = 100

type ForumTopicDiscovery struct {
	Source sqlitestore.TelegramSource
	Topics []sqlitestore.WorkspaceTopic
}

func (c *Client) DiscoverForumTopics(ctx context.Context, store *sqlitestore.Store, configuredRef string, limit int) (ForumTopicDiscovery, error) {
	if store == nil {
		return ForumTopicDiscovery{}, fmt.Errorf("store is required")
	}
	if err := c.validateRuntime(); err != nil {
		return ForumTopicDiscovery{}, err
	}
	if configuredRef == "" {
		return ForumTopicDiscovery{}, fmt.Errorf("workspace legacy source is required")
	}
	if limit <= 0 {
		limit = defaultForumTopicLimit
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return ForumTopicDiscovery{}, err
	}

	client := c.newTelegramClient()
	var discovery ForumTopicDiscovery
	err := client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("telegram session is not authorized; run `sova telegram-login`")
		}

		resolver := peers.Options{}.Build(client.API())
		source, err := c.resolveSyncSource(runCtx, store, client.API(), resolver, configuredRef)
		if err != nil {
			return err
		}
		discovery.Source = source.source
		discovery.Topics, err = fetchForumTopics(runCtx, client.API(), source, limit)
		return err
	})
	if err != nil {
		return ForumTopicDiscovery{}, err
	}
	return discovery, nil
}

func fetchForumTopics(ctx context.Context, api *tg.Client, source resolvedSource, limit int) ([]sqlitestore.WorkspaceTopic, error) {
	var topics []sqlitestore.WorkspaceTopic
	offsetDate := 0
	offsetID := 0
	offsetTopic := 0
	for len(topics) < limit {
		batchLimit := limit - len(topics)
		if batchLimit > defaultForumTopicLimit {
			batchLimit = defaultForumTopicLimit
		}
		response, err := api.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
			Peer:        source.inputPeer,
			OffsetDate:  offsetDate,
			OffsetID:    offsetID,
			OffsetTopic: offsetTopic,
			Limit:       batchLimit,
		})
		if err != nil {
			return nil, err
		}
		if len(response.Topics) == 0 {
			break
		}
		for _, topicClass := range response.Topics {
			topic, ok := convertForumTopic(source.source, topicClass)
			if ok {
				topics = append(topics, topic)
			}
		}
		last, ok := lastForumTopic(response.Topics)
		if !ok {
			break
		}
		offsetDate = last.Date
		offsetID = last.TopMessage
		offsetTopic = last.ID
		if len(response.Topics) < batchLimit || (response.Count > 0 && len(topics) >= response.Count) {
			break
		}
	}
	return topics, nil
}

func convertForumTopic(source sqlitestore.TelegramSource, topicClass tg.ForumTopicClass) (sqlitestore.WorkspaceTopic, bool) {
	switch topic := topicClass.(type) {
	case *tg.ForumTopic:
		createdAt := time.Time{}
		if topic.Date > 0 {
			createdAt = time.Unix(int64(topic.Date), 0).UTC()
		}
		return sqlitestore.WorkspaceTopic{
			SourceRef:    source.Ref,
			ChatID:       source.ChatID,
			TopicID:      topic.ID,
			TopMessageID: topic.TopMessage,
			Title:        topic.Title,
			Pinned:       topic.Pinned,
			Closed:       topic.Closed,
			Hidden:       topic.Hidden,
			CreatedAt:    createdAt,
		}, true
	case *tg.ForumTopicDeleted:
		return sqlitestore.WorkspaceTopic{
			SourceRef: source.Ref,
			ChatID:    source.ChatID,
			TopicID:   topic.ID,
			Title:     "(deleted topic)",
		}, true
	default:
		return sqlitestore.WorkspaceTopic{}, false
	}
}

func lastForumTopic(topics []tg.ForumTopicClass) (*tg.ForumTopic, bool) {
	for i := len(topics) - 1; i >= 0; i-- {
		if topic, ok := topics[i].(*tg.ForumTopic); ok {
			return topic, true
		}
	}
	return nil, false
}
