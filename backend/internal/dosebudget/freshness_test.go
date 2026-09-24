package dosebudget

import (
	"testing"
	"time"

	"radiation-dose-budget-control/backend/internal/constants"
)

func freshnessAt() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC) }

func verifiedEntry(id uint, source string, at time.Time, dose float64, entryType string, correctionOf *uint) ExposureEntryLike {
	return ExposureEntryLike{
		ID: id, SourceRef: source, OccurredAt: at, DoseMSV: dose,
		EntryType: entryType, QualityFlag: constants.QualityFlagVerified, CorrectionOfID: correctionOf,
	}
}

func pendingEntry(id uint, source string, at time.Time, dose float64) ExposureEntryLike {
	return ExposureEntryLike{
		ID: id, SourceRef: source, OccurredAt: at, DoseMSV: dose,
		EntryType: constants.EntryTypeConfirmed, QualityFlag: constants.QualityFlagPending,
	}
}

func TestEvaluateFreshness(t *testing.T) {
	periodStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	at := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	baseInputs := func() FreshnessSnapshotInputs {
		return FreshnessSnapshotInputs{
			PeriodStart: periodStart, PeriodEnd: periodEnd, PeriodDoseMSV: 1.5,
			AdminLimitMSV: 12, LegalLimitMSV: 20, WorkerVersion: 1,
			IncludedExposureIDs: []uint{1, 2}, ExcludedExposureIDs: []uint{3},
		}
	}
	snapshotEntries := func() []ExposureEntryLike {
		return []ExposureEntryLike{
			verifiedEntry(1, "SRC-1", at, 0.5, constants.EntryTypeConfirmed, nil),
			verifiedEntry(2, "SRC-2", at, 1.0, constants.EntryTypeConfirmed, nil),
			pendingEntry(3, "SRC-3", at, 0.8),
		}
	}

	tests := []struct {
		name               string
		entries            []ExposureEntryLike
		currentAdmin       float64
		currentLegal       float64
		currentWorkerVer   uint
		wantFresh          bool
		wantReason         string
		wantDelta          float64
		wantCurrentDose    float64
		wantExposureChange int
		wantWorkerChange   int
		wantNewExcluded    int
	}{
		{
			name:            "unchanged ledger and limits stay fresh",
			entries:         snapshotEntries(),
			currentAdmin:    12,
			currentLegal:    20,
			wantFresh:       true,
			wantDelta:       0,
			wantCurrentDose: 1.5,
		},
		{
			name: "newly verified record makes snapshot stale with positive net delta",
			entries: append(snapshotEntries(),
				verifiedEntry(4, "SRC-4", at, 0.2, constants.EntryTypeConfirmed, nil)),
			currentAdmin:       12,
			currentLegal:       20,
			wantFresh:          false,
			wantReason:         FreshnessReasonExposure,
			wantDelta:          0.2,
			wantCurrentDose:    1.7,
			wantExposureChange: 1,
		},
		{
			name: "reversal and replacement correction changes net dose",
			entries: append(snapshotEntries(),
				verifiedEntry(5, "REV-2", at, -1.0, constants.EntryTypeReversal, uintPtr(2)),
				verifiedEntry(6, "SRC-2-C1", at, 0.7, constants.EntryTypeReplacement, uintPtr(5))),
			currentAdmin:       12,
			currentLegal:       20,
			wantFresh:          false,
			wantReason:         FreshnessReasonExposure,
			wantDelta:          -0.3,
			wantCurrentDose:    1.2,
			wantExposureChange: 2,
		},
		{
			name: "previously verified record leaving evidence is reported as removed",
			entries: []ExposureEntryLike{
				verifiedEntry(1, "SRC-1", at, 0.5, constants.EntryTypeConfirmed, nil),
				pendingEntry(3, "SRC-3", at, 0.8),
			},
			currentAdmin:       12,
			currentLegal:       20,
			wantFresh:          false,
			wantReason:         FreshnessReasonExposure,
			wantDelta:          -1.0,
			wantCurrentDose:    0.5,
			wantExposureChange: 1,
		},
		{
			name: "still pending new record is only counted as a new exclusion",
			entries: append(snapshotEntries(),
				pendingEntry(7, "SRC-7", at, 0.4)),
			currentAdmin:    12,
			currentLegal:    20,
			wantFresh:       true,
			wantDelta:       0,
			wantCurrentDose: 1.5,
			wantNewExcluded: 1,
		},
		{
			name:             "administrative limit adjustment makes snapshot stale",
			entries:          snapshotEntries(),
			currentAdmin:     10,
			currentLegal:     20,
			wantFresh:        false,
			wantReason:       FreshnessReasonLimits,
			wantDelta:        0,
			wantCurrentDose:  1.5,
			wantWorkerChange: 1,
		},
		{
			name:             "legal limit adjustment makes snapshot stale",
			entries:          snapshotEntries(),
			currentAdmin:     12,
			currentLegal:     21,
			wantFresh:        false,
			wantReason:       FreshnessReasonLimits,
			wantCurrentDose:  1.5,
			wantWorkerChange: 1,
		},
		{
			name: "exposure and limit drift together reports combined reason",
			entries: append(snapshotEntries(),
				verifiedEntry(4, "SRC-4", at, 0.2, constants.EntryTypeConfirmed, nil)),
			currentAdmin:       12,
			currentLegal:       22,
			wantFresh:          false,
			wantReason:         FreshnessReasonBoth,
			wantDelta:          0.2,
			wantCurrentDose:    1.7,
			wantExposureChange: 1,
			wantWorkerChange:   1,
		},
		{
			name: "zero net contribution still invalidates a changed evidence set",
			entries: append(snapshotEntries(),
				verifiedEntry(8, "SRC-ZERO", at, 0, constants.EntryTypeConfirmed, nil)),
			currentAdmin:       12,
			currentLegal:       20,
			wantFresh:          false,
			wantReason:         FreshnessReasonExposure,
			wantDelta:          0,
			wantCurrentDose:    1.5,
			wantExposureChange: 1,
		},
		{
			name:             "worker version bump alone does not invalidate the dose basis",
			entries:          snapshotEntries(),
			currentAdmin:     12,
			currentLegal:     20,
			currentWorkerVer: 2,
			wantFresh:        true,
			wantDelta:        0,
			wantCurrentDose:  1.5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputs := baseInputs()
			got := EvaluateFreshness(inputs, test.entries, test.currentAdmin, test.currentLegal, test.currentWorkerVer, freshnessAt())
			if got.Fresh != test.wantFresh {
				t.Fatalf("fresh = %v, want %v (reason %q)", got.Fresh, test.wantFresh, got.StaleReasonCode)
			}
			if got.StaleReasonCode != test.wantReason {
				t.Fatalf("reason = %q, want %q", got.StaleReasonCode, test.wantReason)
			}
			if got.PeriodDoseDeltaMSV != test.wantDelta {
				t.Fatalf("delta = %v, want %v", got.PeriodDoseDeltaMSV, test.wantDelta)
			}
			if got.CurrentPeriodDoseMSV != test.wantCurrentDose {
				t.Fatalf("current dose = %v, want %v", got.CurrentPeriodDoseMSV, test.wantCurrentDose)
			}
			if len(got.ExposureChanges) != test.wantExposureChange {
				t.Fatalf("exposure changes = %d, want %d", len(got.ExposureChanges), test.wantExposureChange)
			}
			if len(got.WorkerChanges) != test.wantWorkerChange {
				t.Fatalf("worker changes = %d, want %d", len(got.WorkerChanges), test.wantWorkerChange)
			}
			if got.NewExcludedEntryCount != test.wantNewExcluded {
				t.Fatalf("new excluded = %d, want %d", got.NewExcludedEntryCount, test.wantNewExcluded)
			}
			if got.Fresh && got.StaleReason != "" {
				t.Fatalf("fresh report must not carry a stale reason, got %q", got.StaleReason)
			}
			if !got.Fresh && got.StaleReason == "" {
				t.Fatal("stale report must explain why it is outdated")
			}
		})
	}
}

func uintPtr(value uint) *uint { return &value }
