package dosebudget

import (
	"math"
	"sort"
	"time"

	"radiation-dose-budget-control/backend/internal/constants"
)

// FreshnessExposureChange describes one verified exposure record whose
// contribution to the frozen period changed after an assessment was calculated.
type FreshnessExposureChange struct {
	EntryID        uint    `json:"entry_id"`
	SourceRef      string  `json:"source_ref"`
	OccurredAt     string  `json:"occurred_at"`
	EntryType      string  `json:"entry_type"`
	ChangeKind     string  `json:"change_kind"` // added | removed
	DoseDeltaMSV   float64 `json:"dose_delta_msv"`
	CorrectionOfID *uint   `json:"correction_of_id,omitempty"`
}

// FreshnessWorkerChange describes a worker profile input that drifted from the
// values frozen inside an assessment snapshot.
type FreshnessWorkerChange struct {
	Field         string  `json:"field"`
	SnapshotValue float64 `json:"snapshot_value"`
	CurrentValue  float64 `json:"current_value"`
	ChangeKind    string  `json:"change_kind"` // limit_changed
}

// Freshness is the deterministic replay of an assessment snapshot against the
// current worker profile and exposure ledger. It never mutates the immutable
// assessment; it only reports whether the frozen evidence is still current.
type Freshness struct {
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

const (
	// FreshnessReasonExposure marks new or removed verified records in the period.
	FreshnessReasonExposure = "verified_exposure_records_changed"
	// FreshnessReasonLimits marks administrative/legal limit adjustments.
	FreshnessReasonLimits = "worker_limits_changed"
	// FreshnessReasonBoth marks both kinds of drift.
	FreshnessReasonBoth = "exposure_records_and_limits_changed"
)

// FreshnessSnapshotInputs are the frozen inputs taken from an assessment row.
type FreshnessSnapshotInputs struct {
	PeriodStart         time.Time
	PeriodEnd           time.Time
	PeriodDoseMSV       float64
	AdminLimitMSV       float64
	LegalLimitMSV       float64
	WorkerVersion       uint
	IncludedExposureIDs []uint
	ExcludedExposureIDs []uint
}

// EvaluateFreshness replays the frozen period window over the current verified
// ledger and worker profile, returning the set of changes and the net period
// dose difference. Record identity drives inclusion; limits are compared with
// fixed epsilon so rounding noise cannot invalidate an assessment.
func EvaluateFreshness(
	inputs FreshnessSnapshotInputs,
	entries []ExposureEntryLike,
	currentAdminLimit, currentLegalLimit float64,
	currentWorkerVersion uint,
	checkedAt time.Time,
) Freshness {
	includedSnapshot := idSet(inputs.IncludedExposureIDs)
	excludedSnapshot := idSet(inputs.ExcludedExposureIDs)

	byID := make(map[uint]ExposureEntryLike, len(entries))
	currentIncluded := map[uint]bool{}
	changes := []FreshnessExposureChange{}
	currentDose := 0.0
	newExcluded := []uint{}
	for _, entry := range entries {
		byID[entry.ID] = entry
		if entry.QualityFlag != constants.QualityFlagVerified {
			if !excludedSnapshot[entry.ID] && !includedSnapshot[entry.ID] {
				newExcluded = append(newExcluded, entry.ID)
			}
			continue
		}
		currentIncluded[entry.ID] = true
		currentDose += entry.DoseMSV
		if !includedSnapshot[entry.ID] {
			changes = append(changes, FreshnessExposureChange{
				EntryID: entry.ID, SourceRef: entry.SourceRef, OccurredAt: entry.OccurredAt.UTC().Format(time.RFC3339),
				EntryType: entry.EntryType, ChangeKind: "added", DoseDeltaMSV: roundDifference(entry.DoseMSV),
				CorrectionOfID: entry.CorrectionOfID,
			})
		}
	}
	// Verified records that left the period evidence set (removed or no longer
	// verified). The immutable ledger normally only grows, but the check stays
	// explicit so removals cannot silently change the accepted dose basis.
	for id := range includedSnapshot {
		if currentIncluded[id] {
			continue
		}
		change := FreshnessExposureChange{EntryID: id, ChangeKind: "removed"}
		if entry, ok := byID[id]; ok {
			change.SourceRef = entry.SourceRef
			change.OccurredAt = entry.OccurredAt.UTC().Format(time.RFC3339)
			change.EntryType = entry.EntryType
			change.CorrectionOfID = entry.CorrectionOfID
			change.DoseDeltaMSV = roundDifference(-entry.DoseMSV)
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].EntryID < changes[j].EntryID })
	sort.Slice(newExcluded, func(i, j int) bool { return newExcluded[i] < newExcluded[j] })

	workerChanges := []FreshnessWorkerChange{}
	if !sameDose(inputs.AdminLimitMSV, currentAdminLimit) {
		workerChanges = append(workerChanges, FreshnessWorkerChange{
			Field: "administrative_limit_msv", SnapshotValue: inputs.AdminLimitMSV,
			CurrentValue: currentAdminLimit, ChangeKind: "limit_changed",
		})
	}
	if !sameDose(inputs.LegalLimitMSV, currentLegalLimit) {
		workerChanges = append(workerChanges, FreshnessWorkerChange{
			Field: "annual_limit_msv", SnapshotValue: inputs.LegalLimitMSV,
			CurrentValue: currentLegalLimit, ChangeKind: "limit_changed",
		})
	}

	currentDose = roundDose(math.Max(0, currentDose))
	freshness := Freshness{
		CheckedAt:             checkedAt.UTC(),
		SnapshotPeriodDoseMSV: roundDose(inputs.PeriodDoseMSV),
		CurrentPeriodDoseMSV:  currentDose,
		PeriodDoseDeltaMSV:    roundDifference(currentDose - inputs.PeriodDoseMSV),
		SnapshotAdminLimitMSV: inputs.AdminLimitMSV,
		CurrentAdminLimitMSV:  currentAdminLimit,
		SnapshotLegalLimitMSV: inputs.LegalLimitMSV,
		CurrentLegalLimitMSV:  currentLegalLimit,
		SnapshotWorkerVersion: inputs.WorkerVersion,
		CurrentWorkerVersion:  currentWorkerVersion,
		ExposureChanges:       changes,
		WorkerChanges:         workerChanges,
		NewExcludedEntryCount: len(newExcluded),
		NewExcludedEntryIDs:   newExcluded,
		Fresh:                 true,
	}
	switch {
	case len(changes) > 0 && len(workerChanges) > 0:
		freshness.markStale(FreshnessReasonBoth)
	case len(changes) > 0:
		freshness.markStale(FreshnessReasonExposure)
	case len(workerChanges) > 0:
		freshness.markStale(FreshnessReasonLimits)
	}
	return freshness
}

func (freshness *Freshness) markStale(code string) {
	freshness.Fresh = false
	freshness.StaleReasonCode = code
	switch code {
	case FreshnessReasonExposure:
		freshness.StaleReason = "Verified exposure records changed after the assessment snapshot; the frozen period dose no longer matches the current ledger."
	case FreshnessReasonLimits:
		freshness.StaleReason = "Worker administrative or legal limits were adjusted after the assessment snapshot; the frozen thresholds are outdated."
	default:
		freshness.StaleReason = "Both verified exposure records and worker limits changed after the assessment snapshot; reassess before accepting."
	}
}

// ExposureEntryLike is the minimal record shape the freshness replay needs,
// keeping the diff independent of the persistence model.
type ExposureEntryLike struct {
	ID             uint
	SourceRef      string
	OccurredAt     time.Time
	DoseMSV        float64
	EntryType      string
	QualityFlag    string
	CorrectionOfID *uint
}

func idSet(ids []uint) map[uint]bool {
	set := make(map[uint]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func sameDose(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

// roundDifference rounds a signed dose difference without clamping, because a
// correction chain can legitimately lower the verified period total.
func roundDifference(value float64) float64 {
	return math.Round(value*1000000) / 1000000
}
