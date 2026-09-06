package control

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestFilterModelsSupportsModelManagerFilters(t *testing.T) {
	models := []ModelResponse{
		{
			ID: "opencode/mimo-v2.5", DisplayName: "MiMo V2.5", Selected: true, Pinned: true,
			Capabilities: model.Capabilities{Tools: true, Vision: true},
			Routes:       []RouteResponse{{Provider: "opencode", Access: model.AccessFree, Enabled: true, Health: RouteHealthResponse{Available: true}}},
		},
		{
			ID: "gemini/flash", DisplayName: "Gemini Flash", Selected: false,
			Capabilities: model.Capabilities{Streaming: true},
			Routes:       []RouteResponse{{Provider: "gemini", Access: model.AccessFreeTier, Enabled: true, Health: RouteHealthResponse{Available: false}}},
		},
	}
	filtered := FilterModels(models, ModelFilters{Provider: "opencode", Access: "free", Capability: "tools", Status: "selected", Search: "mimo"})
	if len(filtered) != 1 || filtered[0].ID != "opencode/mimo-v2.5" {
		t.Fatalf("filtered models = %+v", filtered)
	}
	if got := FilterModels(models, ModelFilters{Status: "available"}); len(got) != 1 || got[0].ID != "opencode/mimo-v2.5" {
		t.Fatalf("available models = %+v", got)
	}
}
