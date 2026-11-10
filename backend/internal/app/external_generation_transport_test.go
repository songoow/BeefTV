package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/operations"
)

func TestExternalGenerationInheritsReferenceTransportBeforeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport map[string]string
		blocked   bool
	}{
		{"inline", map[string]string{"image": "inline", "video": "inline", "audio": "auto"}, false},
		{"default", nil, true},
		{"auto", map[string]string{"image": "auto", "video": "auto"}, true},
		{"url", map[string]string{"image": "url", "video": "url"}, true},
		{"link", map[string]string{"image": "link", "video": "link"}, true},
		{"mixed", map[string]string{"image": "inline", "video": "url"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, registry, canvasID, original := referenceTransportGenerationFixture(t, tc.transport)
			choice, err := svc.BindDomain(nil).ResolveAssistantGenerationModel("video", "selected::video")
			if err != nil {
				t.Fatal(err)
			}
			params, _ := json.Marshal(map[string]any{"canvasId": canvasID, "nodeIds": []string{"output"}, "expectedRevision": canvasRevisionOf(t, svc, canvasID), "expectedModelConfigRevision": choice.Revision})
			_, err = registry.Execute(operations.Request{Op: "canvas.generation.submit", OpID: "transport-" + tc.name, UserID: "local", Caller: operations.ExternalCaller(false), Params: params})
			var tasks []model.Task
			if dbErr := svc.Database().Find(&tasks).Error; dbErr != nil {
				t.Fatal(dbErr)
			}
			if tc.blocked {
				var transportErr *kernel.AppError
				if !errors.As(err, &transportErr) || transportErr.Reason != "reference_media_requires_url" || len(tasks) != 0 {
					t.Fatalf("URL policy must reject local references before admission: tasks=%d err=%v", len(tasks), err)
				}
				if tc.name == "mixed" {
					required, ok := transportErr.Details["requiredReferences"].([]map[string]any)
					if !ok || len(required) != 1 || required[0]["kind"] != "video" {
						t.Fatalf("per-kind transport was lost: %#v", transportErr.Details)
					}
				}
				return
			}
			if err != nil || len(tasks) != 1 || tasks[0].Status != model.TaskStatusQueued {
				t.Fatalf("inline submission: tasks=%d err=%v", len(tasks), err)
			}
			var input generation.Input
			if err := json.Unmarshal([]byte(tasks[0].InputJSON), &input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.Config.ReferenceMediaTransport, tc.transport) || input.Config.BaseURL != "https://selected.invalid" {
				t.Fatalf("selected profile was not frozen into task: %#v", input.Config.ReferenceMediaTransport)
			}
			// Prepare the admitted task's real local bytes without starting a worker
			// or issuing any provider / object-storage request.
			ctx := generation.WithProtocolRegistry(svc.bindGenerationRuntime(context.Background(), generation.CallMeta{UserID: "local"}), svc.protocolRegistry())
			if err := generation.ValidateMediaTransport(ctx, input); err != nil {
				t.Fatal(err)
			}
			if err := generation.HydrateMedia(ctx, "local", &input, generation.MediaHydrationPolicyFor(ctx, input)); err != nil {
				t.Fatal(err)
			}
			if len(input.ReferenceImages) != 1 || len(input.ReferenceVideos) != 1 {
				t.Fatalf("lost references: images=%d videos=%d", len(input.ReferenceImages), len(input.ReferenceVideos))
			}
			for _, media := range []generation.Media{input.ReferenceImages[0], input.ReferenceVideos[0]} {
				prefix := "data:" + media.MimeType + ";base64,"
				data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(media.DataURL, prefix))
				if err != nil || !strings.HasPrefix(media.DataURL, prefix) || !bytes.Equal(data, original[media.ID]) || media.URL != "" {
					t.Fatalf("local %s did not become the original inline bytes: %v", media.ID, err)
				}
			}
		})
	}
}

func referenceTransportGenerationFixture(t *testing.T, transport map[string]string) (*Service, *operations.Registry, string, map[string][]byte) {
	t.Helper()
	svc, canvasID, _ := newAssistantTurnService(t)
	sqlDB, _ := svc.Database().DB()
	sqlDB.SetMaxOpenConns(1)
	inline := map[string]string{"image": "inline", "video": "inline"}
	config, _ := json.Marshal(map[string]any{"videoModel": "selected::video", "channels": []any{
		map[string]any{"id": "other", "enabled": true, "baseUrl": "https://other.invalid", "apiKey": "fixture", "models": []string{"video"}, "modelProfiles": []any{map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": inline}}},
		map[string]any{"id": "selected", "enabled": true, "baseUrl": "https://selected.invalid", "apiKey": "fixture", "referenceMediaTransport": inline, "models": []string{"sibling", "video"}, "modelProfiles": []any{
			map[string]any{"model": "sibling", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": inline},
			map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": transport},
		}},
	}})
	if err := svc.SaveLocalModelConfig(config); err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	videoBytes, err := os.ReadFile(filepath.Join("testdata", "reference-fragmented-30fps.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{"image-ref": pngBytes.Bytes(), "video-ref": videoBytes}
	nodes := []any{map[string]any{"id": "output", "type": "video", "metadata": map[string]any{"model": "selected::video", "prompt": "Follow the saved visual references", "seconds": "5", "size": "16:9", "vquality": "720p", "videoEditOperation": "reference_to_video", "referenceMediaTransport": inline}}}
	edges := []any{}
	for _, media := range []struct{ id, kind, mime, name string }{{"image-ref", "image", "image/png", "reference.png"}, {"video-ref", "video", "video/mp4", "reference.mp4"}} {
		data := original[media.id]
		resource, _, err := svc.storeResource("local", media.kind, media.name, media.mime, int64(len(data)), 32, 32, 1000, bytes.NewReader(data), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		assetID := "asset-" + media.id
		asset, _ := json.Marshal(map[string]any{"id": assetID, "title": media.name, "kind": media.kind, "coverUrl": "", "tags": []string{}, "data": map[string]any{"storageKey": "resource:" + resource.ID, "bytes": resource.Size, "mimeType": media.mime, "width": resource.Width, "height": resource.Height, "durationMs": resource.DurationMs}})
		if _, err := svc.UpsertUserAsset("local", asset); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, map[string]any{"id": media.id, "type": media.kind, "metadata": map[string]any{"assetId": assetID, "storageKey": "resource:" + resource.ID, "mimeType": media.mime}})
		edges = append(edges, map[string]any{"id": "edge-" + media.id, "fromNodeId": media.id, "toNodeId": "output"})
	}
	doc, _ := json.Marshal(map[string]any{"id": canvasID, "revision": canvasRevisionOf(t, svc, canvasID), "nodes": nodes, "connections": edges})
	if _, err := svc.UpsertUserCanvasProject("local", doc); err != nil {
		t.Fatal(err)
	}
	registry := operations.NewRegistry(svc, operations.NewStore(svc.Database()))
	operations.RegisterDefaultOps(registry)
	return svc, registry, canvasID, original
}

func TestProductionReferenceTransportUsesOnlyAvailableCustomModelProfile(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		enabled     bool
		selected    bool
		wantError   bool
	}{
		{"custom", "user", true, true, false},
		{"system", "system", true, true, false},
		{"disabled", "user", false, true, true},
		{"deselected", "user", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newAssistantTurnService(t)
			models := []string{"sibling"}
			if tc.selected {
				models = append(models, "video")
			}
			transport := map[string]string{"image": "inline", "video": "url", "audio": "auto"}
			body, _ := json.Marshal(map[string]any{"channels": []any{map[string]any{"id": "custom", "scope": tc.scope, "enabled": tc.enabled, "models": models, "modelProfiles": []any{map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": transport}}}}})
			if err := svc.SaveLocalModelConfig(body); err != nil {
				t.Fatal(err)
			}
			config, err := svc.assistantGenerationConfig(operations.GenerationTarget{Kind: "video", ModelKey: "custom::video", Parameters: map[string]string{"referenceMediaTransport": "caller-must-not-override"}})
			if tc.wantError {
				if err == nil || operations.AsError(err).Reason != "generation_model_unavailable" {
					t.Fatalf("unavailable model admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.scope == "system" {
				if _, exists := config["referenceMediaTransport"]; exists {
					t.Fatal("system channel inherited a custom transport override")
				}
			} else if !reflect.DeepEqual(config["referenceMediaTransport"], transport) {
				t.Fatalf("profile transport changed: %#v", config["referenceMediaTransport"])
			}
		})
	}
}
