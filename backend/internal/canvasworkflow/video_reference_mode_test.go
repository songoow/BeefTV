package canvasworkflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/mediatools"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

type referenceVideoHost struct {
	CompileHost
	repo      *repository.Repository
	validated []generation.Input
}

func (h *referenceVideoHost) ProviderConfig() (map[string]any, error) {
	return map[string]any{"channels": []any{map[string]any{
		"id": "configured", "baseUrl": "https://example.test", "enabled": true,
		"models": []string{"video-model"}, "modelProfiles": []any{map[string]any{"model": "video-model", "capability": "video", "protocol": "openai-video"}},
	}}}, nil
}
func (h *referenceVideoHost) LocalMode() bool                         { return false }
func (h *referenceVideoHost) Repository() *repository.Repository      { return h.repo }
func (h *referenceVideoHost) DecryptSetting(v string) (string, error) { return v, nil }
func (h *referenceVideoHost) OutputCount(string, string) int          { return 1 }
func (h *referenceVideoHost) ValidateTaskShape(input map[string]any) (string, bool, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return "", false, err
	}
	var parsed generation.Input
	if err = json.Unmarshal(raw, &parsed); err == nil {
		h.validated = append(h.validated, parsed)
	}
	return "", false, err
}

func referenceVideoNode(id string) Node {
	return Node{ID: id, Title: id, Role: "operation", MediaType: "video", OperationID: "generate.video", OperationVersion: 1,
		Config: map[string]any{"modelOption": "configured::video-model", "videoSeconds": 10, "videoEditOperation": "reference_to_video"}}
}

func TestReferenceVideoWorkflowCompilesAndMaterializes(t *testing.T) {
	// Compilation checks tool availability without executing media operations.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mediatools.FFprobePathEnv, executable)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "references.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.Resource{}); err != nil {
		t.Fatal(err)
	}
	host := &referenceVideoHost{repo: repository.New(db)}
	first, second := referenceVideoNode("first"), referenceVideoNode("second")
	first.Config["videoSeconds"] = 11
	second.Config["prompt"] = "角色 @[node:character]；连续性 @[node:last]；动作 @[node:motion-b]"
	d := Definition{Version: 1, PresentationVersion: 1, Nodes: []Node{
		{ID: "character", Title: "角色造型", Role: "input", MediaType: "image"},
		{ID: "motion-a", Title: "前段动作", Role: "input", MediaType: "video"},
		{ID: "motion-b", Title: "后段动作", Role: "input", MediaType: "video"}, first,
		{ID: "last", Title: "实际末帧", Role: "operation", MediaType: "image", OperationID: "video.frame", OperationVersion: 1, Config: map[string]any{"mode": "last"}}, second,
	}, Edges: []Edge{
		{From: "character", To: "first", FromPort: "output", ToPort: "referenceImages"},
		{From: "motion-a", To: "first", FromPort: "output", ToPort: "referenceVideos"},
		{From: "first", To: "last", FromPort: "output", ToPort: "video"},
		{From: "character", To: "second", FromPort: "output", ToPort: "referenceImages"},
		{From: "last", To: "second", FromPort: "output", ToPort: "referenceImages", Order: 1},
		{From: "motion-b", To: "second", FromPort: "output", ToPort: "referenceVideos"},
	}, OutputNodeIDs: []string{"first", "last", "second"}}
	compiled, err := Compile(host, "owner", d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Recipes) != 2 || len(host.validated) != 2 {
		t.Fatalf("recipes=%d validations=%d", len(compiled.Recipes), len(host.validated))
	}
	for _, input := range host.validated {
		if input.Metadata["videoEditOperation"] != "reference_to_video" {
			t.Fatal("capability validation did not receive the explicit reference mode")
		}
	}
	editor := EditorDefinition(d, compiled.Recipes)
	for _, n := range editor.Nodes {
		if n.OperationID == "generate.video" && n.Config["videoEditOperation"] != "reference_to_video" {
			t.Fatal("editor lost the reference mode")
		}
	}
	nodes, steps := map[string]Node{}, map[string]*Step{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	for _, id := range []string{"character", "last", "motion-b"} {
		kind := nodes[id].MediaType
		if err = db.Create(&model.Resource{ID: id + "-resource", UserID: "owner", Kind: kind, Status: model.ResourceStatusReady, MimeType: map[string]string{"image": "image/png", "video": "video/mp4"}[kind]}).Error; err != nil {
			t.Fatal(err)
		}
		steps[id] = &Step{Status: "succeeded", ResourceID: id + "-resource"}
	}
	steps["last"].Status = "pending"
	if _, err = Materialize(host, "owner", "operation", compiled.Recipes["second"], nodes, steps); err == nil {
		t.Fatal("second generation proceeded before the actual frame was ready")
	}
	steps["last"].Status = "succeeded"
	request, err := Materialize(host, "owner", "operation", compiled.Recipes["second"], nodes, steps)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request.Input)
	var input generation.Input
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.ReferenceImages) != 2 || input.ReferenceImages[0].StorageKey != "resource:character-resource" || input.ReferenceImages[1].StorageKey != "resource:last-resource" || len(input.ReferenceVideos) != 1 || input.ReferenceVideos[0].StorageKey != "resource:motion-b-resource" {
		t.Fatalf("incorrect input binding: %s", raw)
	}
	if input.Prompt != "角色 @图片1；连续性 @图片2；动作 @视频1" {
		t.Fatalf("incorrect reference labels: %s", input.Prompt)
	}
	// Hydration normally supplies these URLs; this test only builds the request
	// in memory and never submits to a provider.
	for i := range input.ReferenceImages {
		input.ReferenceImages[i].URL = "https://example.test/image.png"
	}
	input.ReferenceVideos[0].URL = "https://example.test/motion.mp4"
	protocolRequest := generation.ProtocolRequestFromInput(input)
	if protocolRequest.Operation != "reference_to_video" || protocolRequest.Images[0].Role != "reference_image" || protocolRequest.Images[1].Role != "reference_image" || protocolRequest.Videos[0].Role != "reference_video" {
		t.Fatalf("wrong reference roles: %+v", protocolRequest)
	}
}

func TestVideoReferenceModeEditsPreserveOmittedLegacyRecipes(t *testing.T) {
	for _, mode := range []string{"omitted", "reference_to_video", ""} {
		t.Run(map[string]string{"": "automatic", "omitted": "omitted", "reference_to_video": "reference"}[mode], func(t *testing.T) {
			n := referenceVideoNode("video")
			delete(n.Config, "videoEditOperation")
			if mode != "omitted" {
				n.Config["videoEditOperation"] = mode
			}
			d := Definition{Version: 1, Nodes: []Node{n}, OutputNodeIDs: []string{n.ID}}
			metadata := map[string]any{"videoEditOperation": "image_to_video", "videoStartFrameNodeId": "first", "videoEndFrameNodeId": "last", "videoMiddleFrameNodeId": "middle", "videoMiddleFrameTime": 1, "videoDraftTaskId": "previous-draft", "providerOptions": map[string]any{"custom": true}}
			original := RecipesByNode{n.ID: {ChannelID: "configured", Request: RecipeRequest{Type: "canvas_video", Input: map[string]any{"mode": "video", "config": map[string]any{"model": "video-model", "interfaceType": "openai-video", "baseUrl": "https://example.test"}, "metadata": metadata}}}}
			definitionJSON, _ := json.Marshal(d)
			recipesJSON, _ := json.Marshal(original)
			host := &referenceVideoHost{}
			compiled, err := Compile(host, "owner", d, &model.CanvasWorkflow{DefinitionJSON: string(definitionJSON), RecipeCipher: string(recipesJSON)})
			if err != nil {
				t.Fatal(err)
			}
			input := compiled.Recipes[n.ID].Request.Input
			actual := input["metadata"].(map[string]any)
			if mode == "omitted" {
				if actual["videoEditOperation"] != "image_to_video" || actual["videoStartFrameNodeId"] != "first" || actual["videoMiddleFrameNodeId"] != "middle" || actual["videoDraftTaskId"] != "previous-draft" {
					t.Fatalf("legacy frame assignments changed: %+v", actual)
				}
			} else {
				for _, key := range []string{"videoStartFrameNodeId", "videoEndFrameNodeId", "videoMiddleFrameNodeId", "videoMiddleFrameTime", "videoDraftTaskId"} {
					if actual[key] != nil {
						t.Fatalf("stale frame role %s survived", key)
					}
				}
				if mode == "" && actual["videoEditOperation"] != nil || mode != "" && actual["videoEditOperation"] != mode {
					t.Fatalf("wrong explicit mode: %+v", actual)
				}
				protocolRequest := generation.ProtocolRequestFromInput(host.validated[0])
				if protocolRequest.Operation != mode || protocolRequest.Extra["draft_task_id"] != nil {
					t.Fatalf("previous draft overrode the selected mode: %+v", protocolRequest)
				}
			}
			if !reflect.DeepEqual(actual["providerOptions"], metadata["providerOptions"]) {
				t.Fatal("unrelated provider options changed")
			}
			presented := EditorDefinition(d, compiled.Recipes).Nodes[0]
			if mode == "omitted" {
				if _, exists := presented.Config["videoEditOperation"]; exists {
					t.Fatal("editor introduced a new mode for a legacy frame recipe")
				}
			} else if presented.Config["videoEditOperation"] != mode {
				t.Fatal("explicit mode did not survive an editor roundtrip")
			}
		})
	}
}

func TestWorkflowVideoReferenceModeRejectsUnknownOperation(t *testing.T) {
	n := referenceVideoNode("video")
	n.Config["videoEditOperation"] = "unsupported-mode"
	if err := ValidateOperation(n); err == nil || !strings.Contains(err.Error(), "素材使用方式") {
		t.Fatalf("unknown operation accepted: %v", err)
	}
}

func TestDraftVideoReferenceRecipeEditorDoesNotIntroduceModeOverride(t *testing.T) {
	n := referenceVideoNode("video")
	delete(n.Config, "videoEditOperation")
	d := Definition{Version: 1, Nodes: []Node{n}, OutputNodeIDs: []string{n.ID}}
	recipes := RecipesByNode{n.ID: {Request: RecipeRequest{Input: map[string]any{"metadata": map[string]any{"videoEditOperation": "reference_to_video", "videoDraftTaskId": "previous-draft"}}}}}
	presented := EditorDefinition(d, recipes).Nodes[0]
	if _, exists := presented.Config["videoEditOperation"]; exists {
		t.Fatal("opening a legacy draft recipe introduced a mode override")
	}
}
