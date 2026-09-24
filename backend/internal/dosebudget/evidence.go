package dosebudget

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"radiation-dose-budget-control/backend/internal/model"
)

const BoundaryStatement = "Offline ALARA planning result only. It is not a work permit, dosimeter reading, regulatory determination, or medical advice."

type SnapshotEntryState struct {
	ID          uint   `json:"id"`
	QualityFlag string `json:"quality_flag"`
}

type Snapshot struct {
	WorkerID               uint                 `json:"worker_id"`
	WorkerCode             string               `json:"worker_code"`
	WorkerVersion          uint                 `json:"worker_version"`
	PlanID                 uint                 `json:"plan_id"`
	PlanCode               string               `json:"plan_code"`
	PlanVersion            uint                 `json:"plan_version"`
	PeriodStart            time.Time            `json:"period_start"`
	PeriodEnd              time.Time            `json:"period_end"`
	EstimatedRateMSVH      float64              `json:"estimated_rate_msvh"`
	PlannedMinutes         int                  `json:"planned_minutes"`
	Controls               []string             `json:"controls"`
	IncludedExposureIDs    []uint               `json:"included_exposure_ids"`
	ExcludedExposureIDs    []uint               `json:"excluded_exposure_ids"`
	ExcludedExposureStates []SnapshotEntryState `json:"excluded_exposure_states,omitempty"`
	AdministrativeLimitMSV float64              `json:"administrative_limit_msv"`
	LegalLimitMSV          float64              `json:"legal_limit_msv"`
	NearLegalRatio         float64              `json:"near_legal_ratio"`
	ThresholdVersion       string               `json:"threshold_version"`
}

type Evidence struct {
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

func BuildArtifacts(snapshot Snapshot, summary PeriodSummary, decision Decision) (string, string, error) {
	snapshot.Controls = append([]string(nil), snapshot.Controls...)
	sort.Strings(snapshot.Controls)
	snapshot.IncludedExposureIDs = append([]uint(nil), summary.IncludedEntryIDs...)
	snapshot.ExcludedExposureIDs = append([]uint(nil), summary.ExcludedEntryIDs...)
	snapshot.ExcludedExposureStates = make([]SnapshotEntryState, 0, len(summary.ExcludedQualities))
	for _, id := range summary.ExcludedEntryIDs {
		snapshot.ExcludedExposureStates = append(snapshot.ExcludedExposureStates, SnapshotEntryState{ID: id, QualityFlag: summary.ExcludedQualities[id]})
	}
	evidence := Evidence{
		PeriodStart: snapshot.PeriodStart, PeriodEnd: snapshot.PeriodEnd,
		VerifiedEntryCount: summary.VerifiedEntryCount, ExcludedEntryCount: summary.ExcludedEntryCount,
		CorrectedChainCount: summary.CorrectedChainCount,
		Formula:             "period_dose_msv = sum(verified exposure entries, including immutable reversals and replacements)",
		ProjectionFormula:   "projected_dose_msv = period_dose_msv + estimated_rate_msvh * planned_minutes / 60",
		AdministrativeLimit: snapshot.AdministrativeLimitMSV, AnnualLegalLimit: snapshot.LegalLimitMSV,
		NearLegalRatio: snapshot.NearLegalRatio, ThresholdVersion: snapshot.ThresholdVersion,
		RequiresManualReview: decision.RequiresManualReview, EscalationReason: decision.EscalationExplanation,
		BoundaryStatement: BoundaryStatement,
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", "", fmt.Errorf("encode assessment input snapshot: %w", err)
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return "", "", fmt.Errorf("encode assessment evidence: %w", err)
	}
	return string(snapshotJSON), string(evidenceJSON), nil
}

func RiskRank(value string) int {
	ranks := map[string]int{"within_admin": 1, "above_admin": 2, "near_legal": 3, "above_legal": 4, "invalid": 5}
	return ranks[value]
}

// Freshness change categories, stable enough to be used as machine-readable codes.
const (
	ChangeEntryAdded       = "entry_added"
	ChangeEntryRemoved     = "entry_removed"
	ChangeEntryQuality     = "entry_quality_changed"
	ChangeAdminLimit       = "administrative_limit_changed"
	ChangeLegalLimit       = "legal_limit_changed"
	ChangeThresholdVersion = "threshold_version_changed"
)

// Freshness reason codes explain why a frozen snapshot no longer reflects current inputs.
const (
	ReasonExposureRecordsChanged  = "exposure_records_changed"
	ReasonWorkerLimitsChanged     = "worker_limits_changed"
	ReasonThresholdVersionChanged = "threshold_version_changed"
)

// EntryChange describes a single period exposure record that differs from the frozen snapshot.
type EntryChange struct {
	EntryID       uint      `json:"entry_id"`
	SourceRef     string    `json:"source_ref"`
	ChangeType    string    `json:"change_type"`
	QualityFlag   string    `json:"quality_flag"`
	DoseMSV       float64   `json:"dose_msv"`
	OccurredAt    time.Time `json:"occurred_at"`
	SnapshotState string    `json:"snapshot_state"`
	CurrentState  string    `json:"current_state"`
}

// LimitChange describes one configured planning limit that moved after the assessment.
type LimitChange struct {
	LimitName   string  `json:"limit_name"`
	ChangeType  string  `json:"change_type"`
	SnapshotMSV float64 `json:"snapshot_msv"`
	CurrentMSV  float64 `json:"current_msv"`
}

// FreshnessReport captures every way current inputs diverge from an immutable assessment snapshot.
type FreshnessReport struct {
	IsFresh               bool          `json:"is_fresh"`
	CheckedAt             time.Time     `json:"checked_at"`
	SnapshotWorkerVersion uint          `json:"snapshot_worker_version"`
	CurrentWorkerVersion  uint          `json:"current_worker_version"`
	SnapshotPeriodDoseMSV float64       `json:"snapshot_period_dose_msv"`
	CurrentPeriodDoseMSV  float64       `json:"current_period_dose_msv"`
	PeriodDoseDeltaMSV    float64       `json:"period_dose_delta_msv"`
	EntryChanges          []EntryChange `json:"entry_changes"`
	LimitChanges          []LimitChange `json:"limit_changes"`
	ThresholdChanged      bool          `json:"threshold_changed"`
	SnapshotThreshold     string        `json:"snapshot_threshold_version"`
	CurrentThreshold      string        `json:"current_threshold_version"`
	ReasonCodes           []string      `json:"reason_codes"`
	Reasons               []string      `json:"reasons"`
}

// BuildFreshness replays the frozen period against current entries and worker limits.
// The snapshot is never trusted to describe current data; entries are reloaded from storage.
func BuildFreshness(
	snapshot Snapshot,
	snapshotPeriodDoseMSV float64,
	currentWorkerVersion uint,
	currentAdminLimitMSV, currentLegalLimitMSV, currentNearLegalRatio float64,
	currentThresholdVersion string,
	entries []model.ExposureEntry,
	now time.Time,
) (FreshnessReport, error) {
	summary, err := SummarizeEntries(entries)
	if err != nil {
		return FreshnessReport{}, fmt.Errorf("replay current period entries: %w", err)
	}
	report := FreshnessReport{
		IsFresh:               true,
		CheckedAt:             now.UTC(),
		SnapshotWorkerVersion: snapshot.WorkerVersion,
		CurrentWorkerVersion:  currentWorkerVersion,
		SnapshotPeriodDoseMSV: roundDose(snapshotPeriodDoseMSV),
		CurrentPeriodDoseMSV:  summary.DoseMSV,
		PeriodDoseDeltaMSV:    roundSigned(summary.DoseMSV - snapshotPeriodDoseMSV),
		EntryChanges:          []EntryChange{},
		LimitChanges:          []LimitChange{},
		ReasonCodes:           []string{},
		Reasons:               []string{},
	}

	snapshotIncluded := idSet(snapshot.IncludedExposureIDs)
	snapshotExcluded := idSet(snapshot.ExcludedExposureIDs)
	snapshotExcludedQuality := excludedQualityByID(snapshot)
	currentIncluded := idSet(summary.IncludedEntryIDs)
	currentExcluded := idSet(summary.ExcludedEntryIDs)

	// Entries that now participate in the verified dose total: brand-new verified
	// records (including immutable reversal/replacement chains) or records the
	// snapshot excluded but a later quality review admitted.
	for _, entry := range orderedEntries(entries) {
		if !currentIncluded[entry.ID] {
			continue
		}
		change := EntryChange{
			EntryID: entry.ID, SourceRef: entry.SourceRef, QualityFlag: entry.QualityFlag,
			DoseMSV: roundSigned(entry.DoseMSV), OccurredAt: entry.OccurredAt.UTC(),
			CurrentState: "included",
		}
		switch {
		case snapshotIncluded[entry.ID]:
			continue
		case snapshotExcluded[entry.ID]:
			change.ChangeType = ChangeEntryQuality
			change.SnapshotState = "excluded:" + snapshotExcludedQuality[entry.ID]
		default:
			change.ChangeType = ChangeEntryAdded
			change.SnapshotState = "not_present"
		}
		report.EntryChanges = append(report.EntryChanges, change)
	}

	// Records still outside the verified set can still move between pending and
	// rejected via quality review; the dose total is untouched but evidence changed.
	for _, entry := range orderedEntries(entries) {
		if !currentExcluded[entry.ID] || !snapshotExcluded[entry.ID] {
			continue
		}
		snapshotQuality := snapshotExcludedQuality[entry.ID]
		if snapshotQuality != "" && snapshotQuality != entry.QualityFlag {
			report.EntryChanges = append(report.EntryChanges, EntryChange{
				EntryID: entry.ID, SourceRef: entry.SourceRef, ChangeType: ChangeEntryQuality,
				QualityFlag: entry.QualityFlag, DoseMSV: roundSigned(entry.DoseMSV),
				OccurredAt:    entry.OccurredAt.UTC(),
				SnapshotState: "excluded:" + snapshotQuality, CurrentState: "excluded:" + entry.QualityFlag,
			})
		}
	}

	// Rows cannot be deleted, so a snapshot entry missing from the replayed window
	// indicates an out-of-band data repair; surface it instead of silently ignoring it.
	currentIDs := map[uint]bool{}
	for _, entry := range entries {
		currentIDs[entry.ID] = true
	}
	for _, id := range sortedUnion(snapshot.IncludedExposureIDs, snapshot.ExcludedExposureIDs) {
		if currentIDs[id] {
			continue
		}
		state := "not_present"
		if snapshotIncluded[id] {
			state = "included"
		} else if snapshotExcluded[id] {
			state = "excluded:" + snapshotExcludedQuality[id]
		}
		report.EntryChanges = append(report.EntryChanges, EntryChange{
			EntryID: id, ChangeType: ChangeEntryRemoved,
			SnapshotState: state, CurrentState: "not_present",
		})
	}

	if limitsDiffer(snapshot.AdministrativeLimitMSV, currentAdminLimitMSV) {
		report.LimitChanges = append(report.LimitChanges, LimitChange{
			LimitName: "administrative_limit_msv", ChangeType: ChangeAdminLimit,
			SnapshotMSV: roundDose(snapshot.AdministrativeLimitMSV), CurrentMSV: roundDose(currentAdminLimitMSV),
		})
	}
	if limitsDiffer(snapshot.LegalLimitMSV, currentLegalLimitMSV) {
		report.LimitChanges = append(report.LimitChanges, LimitChange{
			LimitName: "annual_legal_limit_msv", ChangeType: ChangeLegalLimit,
			SnapshotMSV: roundDose(snapshot.LegalLimitMSV), CurrentMSV: roundDose(currentLegalLimitMSV),
		})
	}
	if snapshot.ThresholdVersion != currentThresholdVersion {
		report.ThresholdChanged = true
	}

	if len(report.EntryChanges) > 0 {
		report.ReasonCodes = append(report.ReasonCodes, ReasonExposureRecordsChanged)
		report.Reasons = append(report.Reasons,
			fmt.Sprintf("%d period exposure record(s) changed since the assessment snapshot; net period dose difference is %+.3f mSv.",
				len(report.EntryChanges), report.PeriodDoseDeltaMSV))
	} else if report.PeriodDoseDeltaMSV != 0 {
		report.ReasonCodes = append(report.ReasonCodes, ReasonExposureRecordsChanged)
		report.Reasons = append(report.Reasons,
			fmt.Sprintf("replayed period dose differs from the snapshot by %+.3f mSv without a traceable record change.", report.PeriodDoseDeltaMSV))
	}
	if len(report.LimitChanges) > 0 {
		report.ReasonCodes = append(report.ReasonCodes, ReasonWorkerLimitsChanged)
		labels := make([]string, 0, len(report.LimitChanges))
		for _, change := range report.LimitChanges {
			labels = append(labels, fmt.Sprintf("%s %.3f -> %.3f mSv", change.LimitName, change.SnapshotMSV, change.CurrentMSV))
		}
		report.Reasons = append(report.Reasons,
			"Worker planning limits changed after assessment: "+strings.Join(labels, "; ")+".")
	}
	if report.ThresholdChanged || limitsDiffer(snapshot.NearLegalRatio, currentNearLegalRatio) {
		report.ThresholdChanged = true
		report.SnapshotThreshold = snapshot.ThresholdVersion
		report.CurrentThreshold = currentThresholdVersion
		report.ReasonCodes = append(report.ReasonCodes, ReasonThresholdVersionChanged)
		detail := fmt.Sprintf("configured threshold version changed from %s to %s after assessment", snapshot.ThresholdVersion, currentThresholdVersion)
		if limitsDiffer(snapshot.NearLegalRatio, currentNearLegalRatio) {
			detail = fmt.Sprintf("configured near-legal ratio changed from %.2f to %.2f and threshold version from %s to %s after assessment",
				snapshot.NearLegalRatio, currentNearLegalRatio, snapshot.ThresholdVersion, currentThresholdVersion)
		}
		report.Reasons = append(report.Reasons, capitalize(detail)+".")
	}
	report.IsFresh = len(report.Reasons) == 0
	if report.IsFresh {
		report.ReasonCodes = []string{}
		report.Reasons = []string{}
	}
	return report, nil
}

// StaleMessage joins freshness reasons into a single human-readable rejection explanation.
func (report FreshnessReport) StaleMessage() string {
	return "assessment snapshot is stale and cannot be accepted: " + strings.Join(report.Reasons, " ") +
		" Return the assessment for reassessment to evaluate current inputs."
}

func excludedQualityByID(snapshot Snapshot) map[uint]string {
	result := map[uint]string{}
	for _, state := range snapshot.ExcludedExposureStates {
		result[state.ID] = state.QualityFlag
	}
	return result
}

func idSet(ids []uint) map[uint]bool {
	result := make(map[uint]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result
}

func orderedEntries(entries []model.ExposureEntry) []model.ExposureEntry {
	ordered := append([]model.ExposureEntry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return ordered
}

func sortedUnion(groups ...[]uint) []uint {
	seen := map[uint]bool{}
	for _, group := range groups {
		for _, id := range group {
			seen[id] = true
		}
	}
	result := make([]uint, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func limitsDiffer(snapshot, current float64) bool {
	const tolerance = 1e-9
	diff := snapshot - current
	if diff < 0 {
		diff = -diff
	}
	return diff > tolerance
}

// roundSigned rounds without clamping so reversal entries and net deltas can stay negative.
func roundSigned(value float64) float64 {
	return math.Round(value*1000000) / 1000000
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
