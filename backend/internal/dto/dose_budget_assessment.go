package dto

import "time"

type CreateDoseBudgetAssessmentRequest struct {
	PlanID    uint      `json:"plan_id" validate:"required,gt=0"`
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

type AssessmentFreshnessEntryChange struct {
	EntryID       uint      `json:"entry_id"`
	SourceRef     string    `json:"source_ref"`
	ChangeType    string    `json:"change_type"`
	QualityFlag   string    `json:"quality_flag"`
	DoseMSV       float64   `json:"dose_msv"`
	OccurredAt    time.Time `json:"occurred_at"`
	SnapshotState string    `json:"snapshot_state"`
	CurrentState  string    `json:"current_state"`
}

type AssessmentFreshnessLimitChange struct {
	LimitName   string  `json:"limit_name"`
	ChangeType  string  `json:"change_type"`
	SnapshotMSV float64 `json:"snapshot_msv"`
	CurrentMSV  float64 `json:"current_msv"`
}

type AssessmentFreshness struct {
	IsFresh               bool                             `json:"is_fresh"`
	CheckedAt             time.Time                        `json:"checked_at"`
	SnapshotWorkerVersion uint                             `json:"snapshot_worker_version"`
	CurrentWorkerVersion  uint                             `json:"current_worker_version"`
	SnapshotPeriodDoseMSV float64                          `json:"snapshot_period_dose_msv"`
	CurrentPeriodDoseMSV  float64                          `json:"current_period_dose_msv"`
	PeriodDoseDeltaMSV    float64                          `json:"period_dose_delta_msv"`
	EntryChanges          []AssessmentFreshnessEntryChange `json:"entry_changes"`
	LimitChanges          []AssessmentFreshnessLimitChange `json:"limit_changes"`
	ThresholdChanged      bool                             `json:"threshold_changed"`
	SnapshotThreshold     string                           `json:"snapshot_threshold_version"`
	CurrentThreshold      string                           `json:"current_threshold_version"`
	ReasonCodes           []string                         `json:"reason_codes"`
	Reasons               []string                         `json:"reasons"`
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
	Freshness         *AssessmentFreshness   `json:"freshness,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	ReviewedBy        *uint                  `json:"reviewed_by,omitempty"`
	ReviewedAt        *time.Time             `json:"reviewed_at,omitempty"`
	ReviewNote        string                 `json:"review_note"`
}

type ScenarioComparisonResponse struct {
	WorkerID          uint                           `json:"worker_id"`
	PeriodDoseMSV     float64                        `json:"period_dose_msv"`
	Scenarios         []DoseBudgetAssessmentResponse `json:"scenarios"`
	HighestRiskBand   string                         `json:"highest_risk_band"`
	BoundaryStatement string                         `json:"boundary_statement"`
}
