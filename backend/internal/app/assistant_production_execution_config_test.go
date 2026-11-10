package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/modelcatalog"
	"infinite-canvas/backend/internal/operations"
	"infinite-canvas/backend/internal/outbound"
	"infinite-canvas/backend/internal/protocol"
)

func TestExternalGenerationKeepsCustomChannelAuthentication(t *testing.T) {
	svc, registry, canvasID, _ := referenceTransportGenerationFixture(t, map[string]string{"image": "inline", "video": "inline"})
	body, err := svc.ReadLocalModelConfig()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err = json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, raw := range snapshot["channels"].([]any) {
		channel := raw.(map[string]any)
		if channel["id"] == "selected" {
			channel["secretKey"] = "synthetic-signing-secret"
			channel["headers"] = []any{map[string]any{"name": "X-Required-Gateway", "value": "synthetic-header"}}
			channel["referenceAssetOrigin"] = "https://assets.example.test"
		}
	}
	body, _ = json.Marshal(snapshot)
	if err = svc.SaveLocalModelConfig(body); err != nil {
		t.Fatal(err)
	}
	choice, err := svc.BindDomain(nil).ResolveAssistantGenerationModel("video", "selected::video")
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]any{"canvasId": canvasID, "nodeIds": []string{"output"}, "expectedRevision": canvasRevisionOf(t, svc, canvasID), "expectedModelConfigRevision": choice.Revision})
	if _, err = registry.Execute(operations.Request{Op: "canvas.generation.submit", OpID: "custom-auth", UserID: "local", Caller: operations.ExternalCaller(false), Params: params}); err != nil {
		t.Fatal(err)
	}
	var tasks []model.Task
	if err = svc.Database().Find(&tasks).Error; err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d error=%v", len(tasks), err)
	}
	plain, err := svc.decryptTaskInputJSON(tasks[0].InputJSON)
	if err != nil {
		t.Fatal(err)
	}
	var input generation.Input
	if err = json.Unmarshal([]byte(plain), &input); err != nil {
		t.Fatal(err)
	}
	if input.Config.SecretKey != "synthetic-signing-secret" || input.Config.ReferenceAssetOrigin != "https://assets.example.test" || tasks[0].Status != model.TaskStatusQueued {
		t.Fatal("queued external task lost the selected channel's private execution config")
	}
	// Build the authentication portion of a provider request in memory. No
	// worker or HTTP request is started by this admission regression.
	req, err := http.NewRequest(http.MethodPost, "https://selected.invalid/video", nil)
	if err != nil {
		t.Fatal(err)
	}
	outbound.ApplyOutboundHeaders(req, input.Config.Headers)
	if err = generation.ApplyProtocolAuth(req, input.Config, protocol.ManifestAuth{Type: "jwt-hs256", Field: "apiKey", SecretField: "secretKey"}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Required-Gateway") != "synthetic-header" || len(strings.Split(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), ".")) != 3 {
		t.Fatal("configured header or signed authentication did not reach the request")
	}
}

func TestAssistantProductionKeepsVersionedVideoProfileAtExecution(t *testing.T) {
	svc, _, _ := newAssistantTurnService(t)
	profile := modelcatalog.DefaultModelCapabilityConfigForModel("openai-videos", "unseen-video")
	profile.Video.Resolutions, profile.Video.DefaultResolution = []string{"1080p"}, "1080p"
	body, _ := json.Marshal(map[string]any{"channels": []any{map[string]any{
		"id": "custom", "enabled": true, "baseUrl": "https://selected.invalid", "apiKey": "synthetic", "models": []string{"agnes-video-2.5-flash"},
		"modelProfiles": []any{map[string]any{"model": "agnes-video-2.5-flash", "capability": "video", "protocol": "agnes-video", "capabilityConfig": profile, "videoCapabilitiesVersion": "server-v2"}},
	}}})
	if err := svc.SaveLocalModelConfig(body); err != nil {
		t.Fatal(err)
	}
	config, err := svc.assistantGenerationConfig(operations.GenerationTarget{Kind: "video", ModelKey: "custom::agnes-video-2.5-flash", Parameters: map[string]string{"vquality": "1080p"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"mode": "video", "config": config})
	var input generation.Input
	if err = json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if err = generation.ResolveVideoCapability(context.Background(), nil, nil, &input); err != nil {
		t.Fatal(err)
	}
	request := generation.ProtocolRequestFromInput(input)
	if request.Resolution != "1080p" {
		t.Fatalf("versioned provider resolution was replaced by a legacy model default: %q", request.Resolution)
	}
}
