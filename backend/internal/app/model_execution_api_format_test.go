package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/operations"
)

func TestSelectedModelProtocolOverridesChannelFormatAtExecution(t *testing.T) {
	for _, entry := range []string{"assistant", "new workflow", "explicit model resave"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				http.Error(w, "provider submission is not part of this fixture", http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			svc, _, _ := newAssistantTurnService(t)
			svc.mode = serviceModeLocal
			body, _ := json.Marshal(map[string]any{"channels": []any{map[string]any{
				"id": "custom", "enabled": true, "baseUrl": server.URL, "apiKey": "synthetic-only", "apiFormat": "gemini", "models": []string{"video"},
				"modelProfiles": []any{map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2"}},
			}}})
			if err := svc.SaveLocalModelConfig(body); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if entry == "assistant" {
				config, err := svc.assistantGenerationConfig(operations.GenerationTarget{Kind: "video", ModelKey: "custom::video"})
				if err != nil {
					t.Fatal(err)
				}
				value = map[string]any{"mode": "video", "prompt": "synthetic fixture", "config": config}
			} else {
				config := map[string]any{}
				if entry == "explicit model resave" {
					config = map[string]any{"model": "video", "baseUrl": server.URL, "interfaceType": "newapi-channel-2", "apiFormat": "gemini"}
				}
				recipe := workflowRecipe{Request: cw.RecipeRequest{Model: "video", Input: map[string]any{"mode": "video", "prompt": "synthetic fixture", "config": config}}}
				node := cw.Node{MediaType: "video", Config: map[string]any{"modelOption": "custom::video"}}
				if changed, err := svc.selectWorkflowModel(node, &recipe); err != nil || !changed {
					t.Fatalf("selected protocol format was not refreshed: changed=%v error=%v", changed, err)
				}
				request, err := svc.materializeCanvasWorkflowRequest("local", "format-proof", recipe, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				value = request.Input
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var input generation.Input
			if err = json.Unmarshal(encoded, &input); err != nil {
				t.Fatal(err)
			}
			// Use the real executor's format and capability gates, then stop at
			// the submission stage before any provider request can be sent.
			stop := errors.New("fixture stops before provider submission")
			stage := &executionConfigStage{stop: stop}
			ctx := generation.WithRuntime(context.Background(), generation.Runtime{Stages: stage})
			ctx = svc.bindGenerationRuntime(ctx, generation.CallMeta{UserID: "local", TaskID: "format-proof", TaskType: "canvas_video"})
			_, err = generation.Execute(ctx, input)
			if !errors.Is(err, stop) || stage.reached != "正在提交生成任务" || hits.Load() != 0 {
				t.Fatalf("selected model did not reach submission preparation: stage=%q error=%v", stage.reached, err)
			}
			if input.Config.APIFormat != "openai" || input.Config.InterfaceType != "newapi-channel-2" {
				t.Fatal("channel default overrode the selected model's request format")
			}
		})
	}
}

type executionConfigStage struct {
	reached string
	stop    error
}

func (s *executionConfigStage) SetStage(_ context.Context, stage string) error {
	s.reached = stage
	return s.stop
}
