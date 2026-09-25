package workspace

import (
	"context"
	"testing"

	"github.com/SevastyanovYE/Sova/internal/config"
)

func TestCompactControlDoesNotSeedLegacyTopics(t *testing.T) {
	cfg := config.Config{Control: config.ControlConfig{BotToken: "test", ChatID: -1001, Topics: config.ControlTopicIDs{
		Workspace: 6, Nest: 7, TestLab: 5, Status: 1, Errors: 2, Runs: 3, Review: 4, Ideas: 8, Archive: 9,
	}}}
	for _, legacy := range []bool{true, false} {
		if !legacy {
			cfg.Control.Topics = config.ControlTopicIDs{Workspace: 6, Nest: 7, TestLab: 5}
		}
		result, err := SeedControlTopicPins(context.Background(), cfg, SeedTopicPinsOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		specs := ControlTopicSpecs()
		if len(result.Items) != 3 || len(specs) != 3 {
			t.Fatalf("unexpected compact layout: %+v %+v", result, specs)
		}
		for i, topic := range []string{"Workspace", "Nest", "Test Lab"} {
			if result.Items[i].Topic != topic || specs[i].Name != topic || result.Items[i].TopicID != controlTopicID(cfg, topic) {
				t.Fatalf("incorrect topic routing: %+v %+v", result, specs)
			}
		}
	}
}
