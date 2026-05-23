// Package settlement manages the lifecycle of an FX settlement.
//
// A settlement is what happens AFTER a conversion is agreed:
//   1. PENDING   — created, amounts locked
//   2. PROCESSING — funds being moved across accounts
//   3. SETTLED   — complete, irreversible
//   4. FAILED    — something went wrong, needs investigation
//
// This lifecycle mirrors what Atomic would need when a partner (like Aspire
// or Bolt) initiates a cross-currency transaction on behalf of their users.
package settlement

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/mtrishal123/embedded-fx-service/internal/fx"
)

// -------------------------------------------------------------------
// Settlement lifecycle types
// -------------------------------------------------------------------

// Status represents where a settlement is in its lifecycle.
type Status string

const (
	StatusPending    Status = "PENDING"
	StatusProcessing Status = "PROCESSING"
	StatusSettled    Status = "SETTLED"
	StatusFailed     Status = "FAILED"
)

// Settlement is the core domain object. Think of it as a trade ticket
// that records everything about one cross-currency money movement.
type Settlement struct {
	ID              string          // UUID
	PartnerID       string          // which Atomic partner initiated this (e.g. "bolt", "aspire")
	UserID          string          // end user within that partner's platform
	SourceAmount    decimal.Decimal // how much they sent
	SourceCurrency  fx.Currency
	TargetAmount    decimal.Decimal // how much they receive
	TargetCurrency  fx.Currency
	AppliedRate     decimal.Decimal // rate used at time of conversion
	MidMarketRate   decimal.Decimal // for transparency/reporting
	SpreadCost      decimal.Decimal // revenue to Atomic from this settlement
	Status          Status
	FailureReason   string    // populated only if Status == FAILED
	CreatedAt       time.Time
	UpdatedAt       time.Time
	SettledAt       *time.Time // pointer — nil until actually settled
}

// -------------------------------------------------------------------
// Repository interface (the DB abstraction)
// -------------------------------------------------------------------

// Repository defines what the settlement service needs from storage.
// The real implementation uses PostgreSQL; the test implementation uses
// an in-memory map.  This is the key to testable Go code.
type Repository interface {
	Create(s *Settlement) error
	GetByID(id string) (*Settlement, error)
	UpdateStatus(id string, status Status, failureReason string) error
	ListByPartner(partnerID string, limit int) ([]*Settlement, error)
}

// -------------------------------------------------------------------
// Service — business logic layer
// -------------------------------------------------------------------

// Service orchestrates the settlement lifecycle.
// It depends on the FX converter and the repository but doesn't care
// about HTTP or database details — clean separation of concerns.
type Service struct {
	converter  *fx.Converter
	repository Repository
}

// NewService creates a settlement service.
func NewService(converter *fx.Converter, repo Repository) *Service {
	return &Service{
		converter:  converter,
		repository: repo,
	}
}

// CreateSettlement kicks off a new settlement:
//  1. Runs the FX conversion to lock in rate and amounts
//  2. Persists a PENDING settlement record
//  3. Returns the settlement for the caller to present to the user
func (s *Service) CreateSettlement(partnerID, userID string, req fx.ConversionRequest) (*Settlement, error) {
	// Step 1: convert — this locks the rate
	result, err := s.converter.Convert(req)
	if err != nil {
		return nil, fmt.Errorf("create settlement: conversion failed: %w", err)
	}

	// Step 2: build the settlement record
	now := time.Now().UTC()
	settlement := &Settlement{
		ID:             uuid.New().String(),
		PartnerID:      partnerID,
		UserID:         userID,
		SourceAmount:   req.Amount,
		SourceCurrency: req.SourceCurrency,
		TargetAmount:   result.ConvertedAmount,
		TargetCurrency: req.TargetCurrency,
		AppliedRate:    result.AppliedRate,
		MidMarketRate:  result.MidMarketRate,
		SpreadCost:     result.SpreadCost,
		Status:         StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// Step 3: persist
	if err := s.repository.Create(settlement); err != nil {
		return nil, fmt.Errorf("create settlement: persist failed: %w", err)
	}

	return settlement, nil
}

// ProcessSettlement moves a settlement from PENDING → PROCESSING → SETTLED.
// In production this would call a clearing/custody API (e.g. BNY Pershing).
// Here we simulate it — but the state machine is real.
func (s *Service) ProcessSettlement(id string) (*Settlement, error) {
	settlement, err := s.repository.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("process settlement: %w", err)
	}

	// Guard: only PENDING settlements can be processed
	if settlement.Status != StatusPending {
		return nil, fmt.Errorf(
			"process settlement: cannot process settlement in status %q (must be PENDING)",
			settlement.Status,
		)
	}

	// Transition to PROCESSING
	if err := s.repository.UpdateStatus(id, StatusProcessing, ""); err != nil {
		return nil, fmt.Errorf("process settlement: update to PROCESSING failed: %w", err)
	}

	// --- Simulate clearing ---
	// In production: call BNY Pershing / custody API here.
	// On success → SETTLED. On failure → FAILED with reason.
	// For the demo we always succeed.

	now := time.Now().UTC()
	settlement.SettledAt = &now

	if err := s.repository.UpdateStatus(id, StatusSettled, ""); err != nil {
		return nil, fmt.Errorf("process settlement: update to SETTLED failed: %w", err)
	}

	settlement.Status = StatusSettled
	settlement.UpdatedAt = time.Now().UTC()
	return settlement, nil
}

// FailSettlement moves a settlement to FAILED with a reason.
// Called when the downstream clearing step returns an error.
func (s *Service) FailSettlement(id, reason string) error {
	settlement, err := s.repository.GetByID(id)
	if err != nil {
		return fmt.Errorf("fail settlement: %w", err)
	}

	if settlement.Status == StatusSettled {
		return fmt.Errorf("fail settlement: cannot fail an already-settled settlement")
	}

	return s.repository.UpdateStatus(id, StatusFailed, reason)
}

// GetSettlement fetches a settlement by ID.
func (s *Service) GetSettlement(id string) (*Settlement, error) {
	return s.repository.GetByID(id)
}

// ListPartnerSettlements returns recent settlements for a partner.
func (s *Service) ListPartnerSettlements(partnerID string, limit int) ([]*Settlement, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.repository.ListByPartner(partnerID, limit)
}
