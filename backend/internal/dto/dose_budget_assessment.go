package dto

import (
	"time"

	"radiation-dose-budget-control/backend/internal/dosebudget"
)

type CreateDoseBudgetAssessmentRequest struct {
	PlanID    uint      `json:"plan_id" validate:"required,gt=0"`
	PeriodEnd time.Time `json:"period_end" validate:"required"`
	Version   uint      `json:"version" validate:"required,gt=0"`
}

// ReassessDoseBudgetRequest creates a fresh assessment that supersedes an
// assessment currently awaiting RPO review. The plan version proves the
// planner replays the latest committed plan inputs.
type ReassessDoseBudgetRequest struct {
	PeriodEnd time.Time `json:"period_end" validate:"required"`
	Version   uint      `json:"version" validate:"required,gt=0"`
}

type CompareDoseBudgetRequest struct {
	PlanIDs   []uint    `json:"plan_ids" validate:"required,min=2,max=8,dive,gt=0"`
	PeriodEnd time.Time `json:"period_end" validate:"required"`
}

type AssessmentReviewRequest struct {
	Decision string `json:"decision" validate:"required,oneof=accept reject"`
	Note     string `json:"note" validate:"required,min=3,max=1000"`
	Version  uint   `json:"version" validate:"required,gt=0"`
}

type DoseEvidence struct {
	PeriodStart          time.Time `json:"period_start"`
	PeriodEnd            time.Time `json:"period_end"`
	VerifiedEntryCount   int       `json:"verified_entry_count"`
	ExcludedEntryCount   int       `json:"excluded_entry_count"`
	CorrectedChainCount  int       `json:"corrected_chain_count"`
	Formula              string    `json:"formula"`
	ProjectionFormula    string    `json:"projection_formula"`
	AdministrativeLimit  float64   `json:"administrative_limit_msv"`
	AnnualLegalLimit     float64   `json:"annual_legal_limit_msv"`
	NearLegalRatio       float64   `json:"near_legal_ratio"`
	ThresholdVersion     string    `json:"threshold_version"`
	RequiresManualReview bool      `json:"requires_manual_review"`
	EscalationReason     string    `json:"escalation_reason"`
	BoundaryStatement    string    `json:"boundary_statement"`
}

type FreshnessExposureChange struct {
	EntryID        uint    `json:"entry_id"`
	SourceRef      string  `json:"source_ref"`
	OccurredAt     string  `json:"occurred_at"`
	EntryType      string  `json:"entry_type"`
	ChangeKind     string  `json:"change_kind"`
	DoseDeltaMSV   float64 `json:"dose_delta_msv"`
	CorrectionOfID *uint   `json:"correction_of_id,omitempty"`
}

type FreshnessWorkerChange struct {
	Field         string  `json:"field"`
	SnapshotValue float64 `json:"snapshot_value"`
	CurrentValue  float64 `json:"current_value"`
	ChangeKind    string  `json:"change_kind"`
}

// FreshnessReport tells a reviewer whether the frozen snapshot still matches
// the current exposure ledger and worker limits, and what drifted if not.
type FreshnessReport struct {
	CheckedAt             time.Time                 `json:"checked_at"`
	Fresh                 bool                      `json:"fresh"`
	StaleReasonCode       string                    `json:"stale_reason_code,omitempty"`
	StaleReason           string                    `json:"stale_reason,omitempty"`
	SnapshotPeriodDoseMSV float64                   `json:"snapshot_period_dose_msv"`
	CurrentPeriodDoseMSV  float64                   `json:"current_period_dose_msv"`
	PeriodDoseDeltaMSV    float64                   `json:"period_dose_delta_msv"`
	SnapshotAdminLimitMSV float64                   `json:"snapshot_admin_limit_msv"`
	CurrentAdminLimitMSV  float64                   `json:"current_admin_limit_msv"`
	SnapshotLegalLimitMSV float64                   `json:"snapshot_legal_limit_msv"`
	CurrentLegalLimitMSV  float64                   `json:"current_legal_limit_msv"`
	SnapshotWorkerVersion uint                      `json:"snapshot_worker_version"`
	CurrentWorkerVersion  uint                      `json:"current_worker_version"`
	ExposureChanges       []FreshnessExposureChange `json:"exposure_changes"`
	WorkerChanges         []FreshnessWorkerChange   `json:"worker_changes"`
	NewExcludedEntryCount int                       `json:"new_excluded_entry_count"`
	NewExcludedEntryIDs   []uint                    `json:"new_excluded_entry_ids"`
}

type DoseBudgetAssessmentResponse struct {
	ID                uint                   `json:"id"`
	WorkerID          uint                   `json:"worker_id"`
	WorkerCode        string                 `json:"worker_code"`
	WorkerName        string                 `json:"worker_name"`
	PlanID            uint                   `json:"plan_id"`
	PlanCode          string                 `json:"plan_code"`
	AssessmentStatus  string                 `json:"assessment_status"`
	InputSnapshot     map[string]interface{} `json:"input_snapshot"`
	PeriodDoseMSV     float64                `json:"period_dose_msv"`
	ProjectedDoseMSV  float64                `json:"projected_dose_msv"`
	RemainingAdminMSV float64                `json:"remaining_admin_msv"`
	RemainingLegalMSV float64                `json:"remaining_legal_msv"`
	RiskBand          string                 `json:"risk_band"`
	Evidence          DoseEvidence           `json:"evidence"`
	ThresholdVersion  string                 `json:"threshold_version"`
	PlanVersion       uint                   `json:"plan_version"`
	WorkerVersion     uint                   `json:"worker_version"`
	Freshness         *FreshnessReport       `json:"freshness,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	ReviewedBy        *uint                  `json:"reviewed_by,omitempty"`
	ReviewedAt        *time.Time             `json:"reviewed_at,omitempty"`
	ReviewNote        string                 `json:"review_note"`
}

func FreshnessReportFrom(freshness dosebudget.Freshness) FreshnessReport {
	changes := make([]FreshnessExposureChange, 0, len(freshness.ExposureChanges))
	for _, change := range freshness.ExposureChanges {
		changes = append(changes, FreshnessExposureChange{
			EntryID: change.EntryID, SourceRef: change.SourceRef, OccurredAt: change.OccurredAt,
			EntryType: change.EntryType, ChangeKind: change.ChangeKind,
			DoseDeltaMSV: change.DoseDeltaMSV, CorrectionOfID: change.CorrectionOfID,
		})
	}
	workerChanges := make([]FreshnessWorkerChange, 0, len(freshness.WorkerChanges))
	for _, change := range freshness.WorkerChanges {
		workerChanges = append(workerChanges, FreshnessWorkerChange{
			Field: change.Field, SnapshotValue: change.SnapshotValue,
			CurrentValue: change.CurrentValue, ChangeKind: change.ChangeKind,
		})
	}
	excluded := freshness.NewExcludedEntryIDs
	if excluded == nil {
		excluded = []uint{}
	}
	return FreshnessReport{
		CheckedAt: freshness.CheckedAt, Fresh: freshness.Fresh,
		StaleReasonCode: freshness.StaleReasonCode, StaleReason: freshness.StaleReason,
		SnapshotPeriodDoseMSV: freshness.SnapshotPeriodDoseMSV, CurrentPeriodDoseMSV: freshness.CurrentPeriodDoseMSV,
		PeriodDoseDeltaMSV: freshness.PeriodDoseDeltaMSV, SnapshotAdminLimitMSV: freshness.SnapshotAdminLimitMSV,
		CurrentAdminLimitMSV: freshness.CurrentAdminLimitMSV, SnapshotLegalLimitMSV: freshness.SnapshotLegalLimitMSV,
		CurrentLegalLimitMSV: freshness.CurrentLegalLimitMSV, SnapshotWorkerVersion: freshness.SnapshotWorkerVersion,
		CurrentWorkerVersion: freshness.CurrentWorkerVersion, ExposureChanges: changes, WorkerChanges: workerChanges,
		NewExcludedEntryCount: freshness.NewExcludedEntryCount, NewExcludedEntryIDs: excluded,
	}
}

type ScenarioComparisonResponse struct {
	WorkerID          uint                           `json:"worker_id"`
	PeriodDoseMSV     float64                        `json:"period_dose_msv"`
	Scenarios         []DoseBudgetAssessmentResponse `json:"scenarios"`
	HighestRiskBand   string                         `json:"highest_risk_band"`
	BoundaryStatement string                         `json:"boundary_statement"`
}
