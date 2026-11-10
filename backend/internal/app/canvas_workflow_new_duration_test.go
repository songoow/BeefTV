package app

import (
	"encoding/json"
	"strconv"
	"testing"

	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/model"
)

func TestCanvasWorkflowNewDraftFollowsReferenceDuration(t *testing.T) {
	svc, definition := newWorkflowDurationDraftFixture(t)
	template, err := svc.workflowTemplates().Create("owner", cw.CreateRequest{Name: "Follow motion duration", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	editor, err := svc.workflowTemplates().Editor("owner", template.ID, template.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range editor.Workflow.Definition.Nodes {
		if node.ID == "video" && (node.SourceTaskID != "" || node.VideoDurationSourceNodeID != "motion" || node.Config["videoSeconds"] != float64(11)) {
			t.Fatalf("new draft lost its duration configuration: %+v", node)
		}
	}
	saved, err := svc.workflowTemplates().Update("owner", template.ID, cw.UpdateRequest{Name: template.Name, ExpectedRevision: template.Revision, Definition: editor.Workflow.Definition})
	if err != nil {
		t.Fatal(err)
	}
	var row model.CanvasWorkflow
	if err := svc.Database().First(&row, "id = ?", saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	plain, err := svc.decryptSettingSecret(row.RecipeCipher)
	if err != nil {
		t.Fatal(err)
	}
	var recipes workflowRecipes
	if err := json.Unmarshal([]byte(plain), &recipes); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]cw.Node{}
	for _, node := range saved.Definition.Nodes {
		nodes[node.ID] = node
	}
	for _, duration := range []int64{6200, 7001} {
		if err := svc.Database().Model(&model.Resource{}).Where("id = ?", "new-depth").Update("duration_ms", duration).Error; err != nil {
			t.Fatal(err)
		}
		request, err := svc.materializeCanvasWorkflowRequest("owner", "new-duration-run", recipes["video"], nodes, map[string]*cw.Step{"motion": {Status: "succeeded", ResourceID: "new-depth"}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(request.Input)
		if err != nil {
			t.Fatal(err)
		}
		var input generation.Input
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if input.Config.VideoSeconds != strconv.FormatInt((duration+999)/1000, 10) || len(input.ReferenceVideos) != 1 || input.ReferenceVideos[0].StorageKey != "resource:new-depth" || input.ReferenceVideos[0].DurationMs != duration {
			t.Fatalf("materialized duration did not follow the current reference: seconds=%q videos=%+v", input.Config.VideoSeconds, input.ReferenceVideos)
		}
		if input.Prompt != "Follow @视频1" || input.Metadata["videoEditOperation"] != "reference_to_video" {
			t.Fatal("duration binding changed reference semantics")
		}
	}
}

func TestCanvasWorkflowNewDraftDurationRequiresADeclaredParameter(t *testing.T) {
	svc, definition := newWorkflowDurationDraftFixture(t)
	delete(definition.Nodes[1].Config, "videoSeconds")
	if _, err := svc.workflowTemplates().Create("owner", cw.CreateRequest{Name: "Missing duration", Definition: definition}); err == nil {
		t.Fatal("duration-following draft without a duration parameter was accepted")
	}
	definition.Nodes[1].VideoDurationSourceNodeID = ""
	if _, err := svc.workflowTemplates().Create("owner", cw.CreateRequest{Name: "Provider duration default", Definition: definition}); err != nil {
		t.Fatalf("ordinary new draft was rejected after removing duration following: %v", err)
	}
}

func newWorkflowDurationDraftFixture(t *testing.T) (*Service, cw.Definition) {
	t.Helper()
	svc, _, _ := workflowFixture(t)
	svc.mode = serviceModeLocal
	if err := svc.SaveLocalModelConfig([]byte(`{"channels":[{"id":"custom","enabled":true,"baseUrl":"https://provider.invalid","apiKey":"fixture-only","models":["video"],"modelProfiles":[{"model":"video","capability":"video","protocol":"newapi-channel-2","referenceMediaTransport":{"video":"inline"}}]}]}`)); err != nil {
		t.Fatal(err)
	}
	definition := cw.Definition{Version: 1, PresentationVersion: 1, Nodes: []cw.Node{
		{ID: "motion", Title: "Motion", Role: "input", MediaType: "video"},
		{ID: "video", Title: "Video", Role: "operation", MediaType: "video", OperationID: "generate.video", OperationVersion: 1, VideoDurationSourceNodeID: "motion", Config: map[string]any{"modelOption": "custom::video", "videoSeconds": float64(11), "videoEditOperation": "reference_to_video", "prompt": "Follow @[node:motion]"}},
	}, Edges: []cw.Edge{{From: "motion", To: "video", FromPort: "output", ToPort: "referenceVideos"}}, OutputNodeIDs: []string{"video"}}
	return svc, definition
}
