package settlement

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/mtrishal123/embedded-fx-service/internal/fx"
)

// newTestService wires a real converter to an in-memory repository, so the
// tests exercise the full create -> process flow without any infrastructure.
func newTestService() (*Service, *MemoryRepository) {
	repo := NewMemoryRepository()
	converter := fx.NewConverter(fx.NewStaticRateProvider())
	return NewService(converter, repo), repo
}

func sampleRequest() fx.ConversionRequest {
	return fx.ConversionRequest{
		Amount:         decimal.NewFromInt(1000),
		SourceCurrency: fx.EUR,
		TargetCurrency: fx.USD,
		Direction:      fx.Buy,
	}
}

func TestCreateSettlement(t *testing.T) {
	svc, _ := newTestService()

	s, err := svc.CreateSettlement("bolt", "user-1", sampleRequest())
	if err != nil {
		t.Fatalf("create settlement: %v", err)
	}

	if s.ID == "" {
		t.Error("expected a generated ID")
	}
	if s.Status != StatusPending {
		t.Errorf("new settlement should be PENDING, got %s", s.Status)
	}
	if s.PartnerID != "bolt" {
		t.Errorf("partner id: want bolt, got %s", s.PartnerID)
	}
	if !s.TargetAmount.GreaterThan(decimal.Zero) {
		t.Errorf("expected positive target amount, got %s", s.TargetAmount)
	}
}

// TestSettlementLifecycle walks the happy path: PENDING -> PROCESSING -> SETTLED.
func TestSettlementLifecycle(t *testing.T) {
	svc, _ := newTestService()

	created, err := svc.CreateSettlement("aspire", "user-2", sampleRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	processed, err := svc.ProcessSettlement(created.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed.Status != StatusSettled {
		t.Errorf("after processing, status should be SETTLED, got %s", processed.Status)
	}

	// Confirm it persisted as SETTLED.
	fetched, err := svc.GetSettlement(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Status != StatusSettled {
		t.Errorf("persisted status should be SETTLED, got %s", fetched.Status)
	}
	if fetched.SettledAt == nil {
		t.Error("expected SettledAt to be stamped after settlement")
	}
}

// TestCannotProcessTwice guards the state machine: a settled settlement
// cannot be processed again.
func TestCannotProcessTwice(t *testing.T) {
	svc, _ := newTestService()

	created, _ := svc.CreateSettlement("bolt", "user-3", sampleRequest())
	if _, err := svc.ProcessSettlement(created.ID); err != nil {
		t.Fatalf("first process should succeed: %v", err)
	}
	if _, err := svc.ProcessSettlement(created.ID); err == nil {
		t.Error("processing an already-settled settlement should fail")
	}
}

func TestFailSettlement(t *testing.T) {
	svc, _ := newTestService()

	created, _ := svc.CreateSettlement("bolt", "user-4", sampleRequest())
	if err := svc.FailSettlement(created.ID, "clearing rejected"); err != nil {
		t.Fatalf("fail settlement: %v", err)
	}

	fetched, _ := svc.GetSettlement(created.ID)
	if fetched.Status != StatusFailed {
		t.Errorf("status should be FAILED, got %s", fetched.Status)
	}
	if fetched.FailureReason != "clearing rejected" {
		t.Errorf("failure reason not recorded, got %q", fetched.FailureReason)
	}

	// A settled settlement can't be failed.
	settled, _ := svc.CreateSettlement("bolt", "user-5", sampleRequest())
	svc.ProcessSettlement(settled.ID)
	if err := svc.FailSettlement(settled.ID, "too late"); err == nil {
		t.Error("failing an already-settled settlement should error")
	}
}

func TestListPartnerSettlements(t *testing.T) {
	svc, _ := newTestService()

	for i := 0; i < 3; i++ {
		if _, err := svc.CreateSettlement("bolt", "user-x", sampleRequest()); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	_, _ = svc.CreateSettlement("aspire", "user-y", sampleRequest())

	boltList, err := svc.ListPartnerSettlements("bolt", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(boltList) != 3 {
		t.Errorf("expected 3 bolt settlements, got %d", len(boltList))
	}
}

func TestGetSettlement_NotFound(t *testing.T) {
	svc, _ := newTestService()
	if _, err := svc.GetSettlement("does-not-exist"); err == nil {
		t.Error("expected an error for a missing settlement")
	}
}
