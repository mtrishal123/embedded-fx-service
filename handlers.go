// Package api contains the HTTP handlers for the FX settlement service.
//
// Endpoints:
//   POST /v1/settlements           — create a new settlement
//   GET  /v1/settlements/{id}      — get settlement by ID
//   POST /v1/settlements/{id}/process — process (settle) a settlement
//   GET  /v1/partners/{id}/settlements — list settlements for a partner
//   GET  /v1/rates                  — list current FX rates
//   GET  /healthz                   — health check (for k8s liveness probe)
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/shopspring/decimal"

	"github.com/trishal/fx-settlement/internal/fx"
	"github.com/trishal/fx-settlement/internal/settlement"
)

// -------------------------------------------------------------------
// Request / Response types
// -------------------------------------------------------------------

type createSettlementRequest struct {
	PartnerID      string `json:"partner_id"`
	UserID         string `json:"user_id"`
	Amount         string `json:"amount"`          // string to preserve precision
	SourceCurrency string `json:"source_currency"` // e.g. "EUR"
	TargetCurrency string `json:"target_currency"` // e.g. "USD"
	Direction      string `json:"direction"`       // "BUY" or "SELL"
}

type settlementResponse struct {
	ID             string     `json:"id"`
	PartnerID      string     `json:"partner_id"`
	UserID         string     `json:"user_id"`
	SourceAmount   string     `json:"source_amount"`
	SourceCurrency string     `json:"source_currency"`
	TargetAmount   string     `json:"target_amount"`
	TargetCurrency string     `json:"target_currency"`
	AppliedRate    string     `json:"applied_rate"`
	MidMarketRate  string     `json:"mid_market_rate"`
	SpreadCost     string     `json:"spread_cost"`
	Status         string     `json:"status"`
	FailureReason  string     `json:"failure_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	SettledAt      *time.Time `json:"settled_at,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// -------------------------------------------------------------------
// Handler
// -------------------------------------------------------------------

// Handler holds the dependencies for all HTTP handlers.
type Handler struct {
	settlementService *settlement.Service
	rateProvider      *fx.StaticRateProvider
}

// NewHandler creates the handler with its dependencies.
func NewHandler(svc *settlement.Service, rateProvider *fx.StaticRateProvider) *Handler {
	return &Handler{
		settlementService: svc,
		rateProvider:      rateProvider,
	}
}

// RegisterRoutes attaches all routes to the given router.
func (h *Handler) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/healthz", h.healthCheck).Methods(http.MethodGet)
	r.HandleFunc("/v1/rates", h.listRates).Methods(http.MethodGet)
	r.HandleFunc("/v1/settlements", h.createSettlement).Methods(http.MethodPost)
	r.HandleFunc("/v1/settlements/{id}", h.getSettlement).Methods(http.MethodGet)
	r.HandleFunc("/v1/settlements/{id}/process", h.processSettlement).Methods(http.MethodPost)
	r.HandleFunc("/v1/partners/{partnerID}/settlements", h.listPartnerSettlements).Methods(http.MethodGet)
}

// -------------------------------------------------------------------
// Handlers
// -------------------------------------------------------------------

func (h *Handler) healthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) listRates(w http.ResponseWriter, r *http.Request) {
	pairs := h.rateProvider.SupportedPairs()
	type rateItem struct {
		Pair      string `json:"pair"`
		Direction string `json:"direction"`
	}
	items := make([]rateItem, len(pairs))
	for i, p := range pairs {
		items[i] = rateItem{Pair: p, Direction: "available"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rates":     items,
		"note":      "Use POST /v1/settlements to get live conversion amounts",
		"fetchedAt": time.Now().UTC(),
	})
}

// POST /v1/settlements
func (h *Handler) createSettlement(w http.ResponseWriter, r *http.Request) {
	var req createSettlementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_BODY")
		return
	}

	// Validate required fields
	if req.PartnerID == "" || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "partner_id and user_id are required", "MISSING_FIELD")
		return
	}

	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid amount: %q", req.Amount), "INVALID_AMOUNT")
		return
	}

	source, err := fx.ValidateCurrency(req.SourceCurrency)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "INVALID_CURRENCY")
		return
	}
	target, err := fx.ValidateCurrency(req.TargetCurrency)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "INVALID_CURRENCY")
		return
	}

	dir := fx.Direction(req.Direction)
	if dir != fx.Buy && dir != fx.Sell {
		writeError(w, http.StatusBadRequest, `direction must be "BUY" or "SELL"`, "INVALID_DIRECTION")
		return
	}

	convReq := fx.ConversionRequest{
		Amount:         amount,
		SourceCurrency: source,
		TargetCurrency: target,
		Direction:      dir,
	}

	s, err := h.settlementService.CreateSettlement(req.PartnerID, req.UserID, convReq)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error(), "CONVERSION_FAILED")
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(s))
}

// GET /v1/settlements/{id}
func (h *Handler) getSettlement(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	s, err := h.settlementService.GetSettlement(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, toResponse(s))
}

// POST /v1/settlements/{id}/process
func (h *Handler) processSettlement(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	s, err := h.settlementService.ProcessSettlement(id)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error(), "PROCESS_FAILED")
		return
	}
	writeJSON(w, http.StatusOK, toResponse(s))
}

// GET /v1/partners/{partnerID}/settlements
func (h *Handler) listPartnerSettlements(w http.ResponseWriter, r *http.Request) {
	partnerID := mux.Vars(r)["partnerID"]
	settlements, err := h.settlementService.ListPartnerSettlements(partnerID, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "LIST_FAILED")
		return
	}
	items := make([]settlementResponse, len(settlements))
	for i, s := range settlements {
		items[i] = toResponse(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"partner_id":  partnerID,
		"settlements": items,
		"count":       len(items),
	})
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

func toResponse(s *settlement.Settlement) settlementResponse {
	return settlementResponse{
		ID:             s.ID,
		PartnerID:      s.PartnerID,
		UserID:         s.UserID,
		SourceAmount:   s.SourceAmount.String(),
		SourceCurrency: string(s.SourceCurrency),
		TargetAmount:   s.TargetAmount.StringFixed(4),
		TargetCurrency: string(s.TargetCurrency),
		AppliedRate:    s.AppliedRate.StringFixed(6),
		MidMarketRate:  s.MidMarketRate.StringFixed(6),
		SpreadCost:     s.SpreadCost.StringFixed(4),
		Status:         string(s.Status),
		FailureReason:  s.FailureReason,
		CreatedAt:      s.CreatedAt,
		SettledAt:      s.SettledAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, errorResponse{Error: message, Code: code})
}
