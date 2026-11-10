package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/kernel"
)

func TestPublicDomainErrorsKeepTheirMeaningAtEveryOperationBoundary(t *testing.T) {
	for _, test := range []struct {
		status int
		reason kernel.ErrorReason
		code   Code
		want   string
	}{
		{400, "reference_media_requires_url", CodeInvalidArgument, "reference_media_requires_url"},
		{404, kernel.ReasonNotFound, CodeNotFound, "not_found"},
		{409, kernel.ReasonConflict, CodeConflict, "stale_revision"},
		{409, "canvas_changed", CodeConflict, "canvas_changed"},
		{412, "generation_model_unavailable", CodePreconditionFailed, "generation_model_unavailable"},
		{403, "foreign_resource", CodePermissionDenied, "foreign_resource"},
		{500, "local_disk_full", CodeInternal, "local_disk_full"},
	} {
		t.Run(test.want, func(t *testing.T) {
			cause := errors.New("private diagnostic fixture")
			details := map[string]any{"requiredReferences": []map[string]any{{"kind": "video", "index": 1}}}
			failure := &kernel.AppError{Status: test.status, Reason: test.reason, Message: "public validation message", Details: details, Cause: cause}
			wrapped := fmt.Errorf("private wrapper fixture: %w", failure)
			for _, normalize := range []func(error) *Error{AsError, func(err error) *Error { return AsError(mapDomainError(err)) }} {
				got := normalize(wrapped)
				if got.Code != test.code || got.Reason != test.want || got.Message != failure.Message || !reflect.DeepEqual(got.Details, details) || HTTPStatus(got.Code) != test.status {
					t.Fatalf("public contract changed: %+v", got)
				}
				if !errors.Is(got, cause) {
					t.Fatal("internal cause chain was lost")
				}
				body, err := json.Marshal(got)
				if err != nil || strings.Contains(string(body), "private") {
					t.Fatalf("internal diagnostic escaped through JSON: %s %v", body, err)
				}
			}
		})
	}
}

func TestOperationErrorsKeepAuthoredMeaningAndHideUnknownDiagnostics(t *testing.T) {
	authored := Conflict("specific_conflict", "public conflict", map[string]any{"revision": 3})
	if got := AsError(fmt.Errorf("private wrapper fixture: %w", authored)); got != authored {
		t.Fatal("existing operation error was replaced")
	}
	unknown := errors.New("private internal connection fixture")
	got := AsError(unknown)
	if got.Code != CodeInternal || got.Reason != "operation_failed" || HTTPStatus(got.Code) != http.StatusInternalServerError || !errors.Is(got, unknown) {
		t.Fatalf("unknown failure lost classification or cause: %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "private") {
		t.Fatalf("unknown diagnostic was exposed: %s %v", body, err)
	}
	if AsError(nil) != nil || mapDomainError(nil) != nil {
		t.Fatal("nil acquired an error wrapper")
	}
}
