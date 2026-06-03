package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/mtrishal123/embedded-fx-service/internal/fx"
	"github.com/mtrishal123/embedded-fx-service/internal/settlement"
)

// newTestServer builds a fully wired handler backed by an in-memory repo and
// returns an httptest router ready to receive requests.
func newTestServer() *mux.Router {
	rateProvider := fx.NewStaticRateProvider()
	converter := fx.NewConverter(rateProvider)
	repo := settlement.NewMemoryRepository()
	svc := settlement.NewService(converter, repo)

	h := NewHandler(svc, rateProvider)
	r := mux.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func TestHealthCheck(t *testing.T) {
	r := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestCreateSettlement_HTTP(t *testing.T) {
	r := newTestServer()

	body := createSettlementRequest{
		PartnerID:      "bolt",
		UserID:         "user-1",
		Amount:         "1000",
		SourceCurrency: "EUR",
		TargetCurrency: "USD",
		Direction:      "BUY",
	}
	rec := postJSON(t, r, "/v1/settlements", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var resp settlementResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Error("expected an ID in the response")
	}
	if resp.Status != "PENDING" {
		t.Errorf("want PENDING, got %s", resp.Status)
	}
}

func TestCreateSettlement_Validation(t *testing.T) {
	r := newTestServer()

	tests := []struct {
		name string
		body createSettlementRequest
	}{
		{
			name: "missing partner",
			body: createSettlementRequest{UserID: "u", Amount: "10", SourceCurrency: "EUR", TargetCurrency: "USD", Direction: "BUY"},
		},
		{
			name: "bad amount",
			body: createSettlementRequest{PartnerID: "p", UserID: "u", Amount: "abc", SourceCurrency: "EUR", TargetCurrency: "USD", Direction: "BUY"},
		},
		{
			name: "bad currency",
			body: createSettlementRequest{PartnerID: "p", UserID: "u", Amount: "10", SourceCurrency: "XXX", TargetCurrency: "USD", Direction: "BUY"},
		},
		{
			name: "bad direction",
			body: createSettlementRequest{PartnerID: "p", UserID: "u", Amount: "10", SourceCurrency: "EUR", TargetCurrency: "USD", Direction: "UP"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postJSON(t, r, "/v1/settlements", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("want 400, got %d", rec.Code)
			}
		})
	}
}

// TestGetAndProcessFlow exercises create -> get -> process over HTTP.
func TestGetAndProcessFlow(t *testing.T) {
	r := newTestServer()

	createRec := postJSON(t, r, "/v1/settlements", createSettlementRequest{
		PartnerID: "aspire", UserID: "u", Amount: "500",
		SourceCurrency: "EUR", TargetCurrency: "GBP", Direction: "BUY",
	})
	var created settlementResponse
	json.Unmarshal(createRec.Body.Bytes(), &created)

	// GET it back.
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/settlements/"+created.ID, nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", getRec.Code)
	}

	// Process it.
	procRec := httptest.NewRecorder()
	r.ServeHTTP(procRec, httptest.NewRequest(http.MethodPost, "/v1/settlements/"+created.ID+"/process", nil))
	if procRec.Code != http.StatusOK {
		t.Fatalf("process: want 200, got %d", procRec.Code)
	}
	var processed settlementResponse
	json.Unmarshal(procRec.Body.Bytes(), &processed)
	if processed.Status != "SETTLED" {
		t.Errorf("want SETTLED, got %s", processed.Status)
	}
}

func TestGetSettlement_NotFound_HTTP(t *testing.T) {
	r := newTestServer()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/settlements/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rec.Code)
	}
}

func postJSON(t *testing.T, r http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
