package app

import (
	"context"
	"encoding/json"
	"testing"

	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/model"
	localtask "infinite-canvas/backend/internal/task"
)

func TestCanvasWorkflowRetryReusesExternallyRecoveredTask(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initialStatus model.TaskStatus
		restored      bool
	}{
		{"failed task recovered", model.TaskStatusFailed, true},
		{"successful task delivery recovered", model.TaskStatusSucceeded, true},
		{"successful task output still unavailable", model.TaskStatusSucceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, definition := workflowFixture(t)
			definition.Nodes = append(definition.Nodes, cw.Node{ID: "frame", Title: "Frame", Role: "operation", MediaType: "image", OperationID: "video.frame", OperationVersion: 1, Config: map[string]any{"seconds": float64(0)}})
			definition.Edges = append(definition.Edges, cw.Edge{From: "gen", To: "frame", FromPort: "output", ToPort: "video"})
			definition.OutputNodeIDs = []string{"gen", "frame"}
			template, err := svc.workflowTemplates().Create("owner", cw.CreateRequest{Name: "Recover then continue", Definition: definition})
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.workflowRuns().Start("owner", template.ID, cw.RunRequest{TemplateRevision: template.Revision, ClientOperationID: "recover-existing-run"})
			if err != nil {
				t.Fatal(err)
			}
			var original model.Task
			if err := db.First(&original, "id = ?", "original").Error; err != nil {
				t.Fatal(err)
			}
			depthOperation, generationOperation := cw.StepOperation(run.ID, "depth"), cw.StepOperation(run.ID, "gen")
			for _, task := range []model.Task{
				{ID: "finished-depth", UserID: "owner", Type: model.TaskTypeDepthCapture, Status: model.TaskStatusSucceeded, ClientOperationID: &depthOperation, ResultJSON: `{"video":{"resourceId":"new-depth"}}`},
				{ID: "recoverable-generation", UserID: "owner", Type: "canvas_video", Status: tc.initialStatus, ClientOperationID: &generationOperation, ProviderRequestID: "existing-provider-job", InputJSON: original.InputJSON, ResultJSON: `{}`},
			} {
				if err := db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Reconcile actual persisted tasks so the workflow enters its ordinary
			// failure state before the task list restores the original result.
			svc.workflowRuns().AdvanceAll(context.Background())
			failed, err := svc.workflowRuns().Get("owner", run.ID)
			if err != nil || failed.Status != "failed" || recoveredWorkflowStep(t, failed, "gen").TaskID != "recoverable-generation" || recoveredWorkflowStep(t, failed, "frame").TaskID != "" {
				t.Fatalf("workflow did not stop at the failed step: %+v %v", failed, err)
			}
			if tc.restored {
				if err := db.Model(&model.Task{}).Where("id = ?", "recoverable-generation").Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": `{"video":{"resourceId":"new-output"}}`, "error": ""}).Error; err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := svc.repo.Task("recoverable-generation")
			if err != nil {
				t.Fatal(err)
			}
			retried, err := svc.workflowRuns().Retry("owner", run.ID)
			if err != nil || retried.Status != "queued" || recoveredWorkflowStep(t, retried, "gen").TaskID != recovered.ID {
				t.Fatalf("workflow could not resume its original task: %+v %v", retried, err)
			}
			svc.workflowRuns().AdvanceAll(context.Background())
			continued, err := svc.workflowRuns().Get("owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			frame := recoveredWorkflowStep(t, continued, "frame")
			if tc.restored {
				generation := recoveredWorkflowStep(t, continued, "gen")
				if continued.Status != "running" || generation.Status != "succeeded" || generation.ResourceID != "new-output" || frame.TaskID == "" {
					t.Fatalf("recovered output did not reach the next step: %+v", continued)
				}
				downstream, err := svc.repo.Task(frame.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				var mediaInput localtask.CanvasMediaInput
				if err := json.Unmarshal([]byte(downstream.InputJSON), &mediaInput); err != nil {
					t.Fatal(err)
				}
				if downstream.Type != model.TaskTypeCanvasMediaTransform || downstream.Status != model.TaskStatusQueued || mediaInput.ResourceID != "new-output" {
					t.Fatalf("downstream task did not bind the recovered video: %+v", downstream)
				}
				// The native worker is outside this scheduler test. Record its owned
				// output and verify the resumed run can finish without any model call.
				if err := db.Create(&model.Resource{ID: "recovered-frame", UserID: "owner", Kind: "image", MimeType: "image/png", Status: model.ResourceStatusReady}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.Task{}).Where("id = ?", frame.TaskID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": `{"image":{"resourceId":"recovered-frame"}}`}).Error; err != nil {
					t.Fatal(err)
				}
				svc.workflowRuns().AdvanceAll(context.Background())
				finished, err := svc.workflowRuns().Get("owner", run.ID)
				if err != nil || finished.Status != "succeeded" || recoveredWorkflowStep(t, finished, "frame").ResourceID != "recovered-frame" || len(finished.Outputs) != 2 {
					t.Fatalf("resumed workflow did not complete: %+v %v", finished, err)
				}
			} else if continued.Status != "failed" || recoveredWorkflowStep(t, continued, "gen").Status != "failed" || frame.TaskID != "" {
				t.Fatalf("missing output was treated as a completed step: %+v", continued)
			}
			stored, err := svc.repo.Task(recovered.ID)
			if err != nil || stored.Status != model.TaskStatusSucceeded || stored.InputJSON != recovered.InputJSON || stored.ProviderRequestID != recovered.ProviderRequestID || stored.ResultJSON != recovered.ResultJSON {
				t.Fatalf("successful task was mutated or resubmitted: %+v %v", stored, err)
			}
			var originalCount, totalCount int64
			if err := db.Model(&model.Task{}).Where("client_operation_id = ?", generationOperation).Count(&originalCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.Task{}).Count(&totalCount).Error; err != nil {
				t.Fatal(err)
			}
			wantCount := int64(3)
			if tc.restored {
				wantCount++ // Only the new native downstream task was admitted.
			}
			if originalCount != 1 || totalCount != wantCount {
				t.Fatalf("unexpected task admission: original=%d total=%d want=%d", originalCount, totalCount, wantCount)
			}
		})
	}
}

func recoveredWorkflowStep(t *testing.T, run cw.Run, nodeID string) cw.Step {
	t.Helper()
	for _, step := range run.Steps {
		if step.NodeID == nodeID {
			return step
		}
	}
	t.Fatalf("missing workflow step %s", nodeID)
	return cw.Step{}
}
