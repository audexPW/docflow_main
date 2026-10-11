package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"docflow/internal/recognize"
)

func TestPublicDocTypeCatalog(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/doctypes", nil)

	(&Server{}).handleListDocTypes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", rr.Code)
	}

	var result struct {
		Types []docTypeView `json:"types"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	expected := map[string]bool{
		"act":               true,
		"contract":          true,
		"power_of_attorney": true,
		"waybill":           true,
		"upd":               true,
		"unknown":           true,
	}

	if len(result.Types) != len(expected) {
		t.Fatalf("got %d types, want %d", len(result.Types), len(expected))
	}

	for _, typ := range result.Types {
		if !expected[typ.Slug] {
			t.Errorf("unexpected or duplicate public type: %q", typ.Slug)
		}
		delete(expected, typ.Slug)

		if typ.Slug == "act" && typ.Title != "Акт" {
			t.Errorf("act title = %q, want Акт", typ.Title)
		}
	}

	if len(expected) != 0 {
		t.Errorf("missing public types: %v", expected)
	}

	if _, ok := recognize.LookupDocType("schet_faktura"); !ok {
		t.Error("internal schet_faktura type was lost")
	}
}
