package dosebudget

import (
	"strings"
	"testing"
	"time"

	"radiation-dose-budget-control/backend/internal/constants"
	"radiation-dose-budget-control/backend/internal/model"
)

func freshnessSnapshot() Snapshot {
	return Snapshot{
		WorkerID: 7, WorkerCode: "W7", WorkerVersion: 1, PlanID: 3, PlanCode: "P3", PlanVersion: 2,
		PeriodStart:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:              time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		IncludedExposureIDs:    []uint{1},
		ExcludedExposureIDs:    []uint{2},
		ExcludedExposureStates: []SnapshotEntryState{{ID: 2, QualityFlag: constants.QualityFlagPending}},
		AdministrativeLimitMSV: 12, LegalLimitMSV: 20, NearLegalRatio: 0.9, ThresholdVersion: "T1",
	}
}

func freshnessEntry(id uint, ref string, dose float64, quality string, at time.Time) model.ExposureEntry {
	return model.ExposureEntry{
		ID: id, WorkerID: 7, SourceRef: ref, OccurredAt: at, DoseMSV: dose,
		EntryType: constants.EntryTypeConfirmed, QualityFlag: quality,
	}
}

func TestBuildFreshness(t *testing.T) {
	periodStart := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	newAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name            string
		snapshot        Snapshot
		snapshotDose    float64
		workerVersion   uint
		adminLimit      float64
		legalLimit      float64
		nearRatio       float64
		threshold       string
		entries         []model.ExposureEntry
		wantFresh       bool
		wantCodes       []string
		wantDelta       float64
		wantChangeCount int
		wantLimitCount  int
	}{
		{
			name:          "unchanged inputs stay fresh",
			snapshot:      freshnessSnapshot(),
			snapshotDose:  5,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagPending, periodStart),
			},
			wantFresh: true, wantDelta: 0,
		},
		{
			name:          "newly verified record makes snapshot stale with positive net delta",
			snapshot:      freshnessSnapshot(),
			snapshotDose:  5,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagPending, periodStart),
				freshnessEntry(3, "C", 2.5, constants.QualityFlagVerified, newAt),
			},
			wantFresh: false, wantCodes: []string{ReasonExposureRecordsChanged},
			wantDelta: 2.5, wantChangeCount: 1,
		},
		{
			name: "pending entry verified later counts as quality change",
			snapshot: func() Snapshot {
				s := freshnessSnapshot()
				return s
			}(),
			snapshotDose:  5,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagVerified, periodStart),
			},
			wantFresh: false, wantCodes: []string{ReasonExposureRecordsChanged},
			wantDelta: 9, wantChangeCount: 1,
		},
		{
			name: "pending entry rejected later is stale even with zero dose delta",
			snapshot: func() Snapshot {
				s := freshnessSnapshot()
				return s
			}(),
			snapshotDose:  5,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagRejected, periodStart),
			},
			wantFresh: false, wantCodes: []string{ReasonExposureRecordsChanged},
			wantDelta: 0, wantChangeCount: 1,
		},
		{
			name: "immutable correction chain landing in period grows the included set",
			snapshot: func() Snapshot {
				s := freshnessSnapshot()
				s.ExcludedExposureIDs = []uint{}
				s.ExcludedExposureStates = []SnapshotEntryState{}
				return s
			}(),
			snapshotDose:  4,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 4, constants.QualityFlagVerified, periodStart),
				func() model.ExposureEntry {
					parent := uint(1)
					e := freshnessEntry(4, "A-REV", -4, constants.QualityFlagVerified, periodStart)
					e.EntryType = constants.EntryTypeReversal
					e.CorrectionOfID = &parent
					return e
				}(),
				func() model.ExposureEntry {
					parent := uint(4)
					e := freshnessEntry(5, "A-C1", 1.5, constants.QualityFlagVerified, periodStart)
					e.EntryType = constants.EntryTypeReplacement
					e.CorrectionOfID = &parent
					return e
				}(),
			},
			wantFresh: false, wantCodes: []string{ReasonExposureRecordsChanged},
			wantDelta: -2.5, wantChangeCount: 2,
		},
		{
			name:          "adjusted worker limits are reported per limit",
			snapshot:      freshnessSnapshot(),
			snapshotDose:  5,
			workerVersion: 2,
			adminLimit:    10, legalLimit: 18, nearRatio: 0.9, threshold: "T1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagPending, periodStart),
			},
			wantFresh: false,
			wantCodes: []string{ReasonWorkerLimitsChanged},
			wantDelta: 0, wantLimitCount: 2,
		},
		{
			name:          "threshold version drift is its own reason",
			snapshot:      freshnessSnapshot(),
			snapshotDose:  5,
			workerVersion: 1,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.9, threshold: "ALARA-2027.1",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagPending, periodStart),
			},
			wantFresh: false,
			wantCodes: []string{ReasonThresholdVersionChanged},
			wantDelta: 0,
		},
		{
			name:          "combined changes list every reason once",
			snapshot:      freshnessSnapshot(),
			snapshotDose:  5,
			workerVersion: 3,
			adminLimit:    12, legalLimit: 20, nearRatio: 0.95, threshold: "T2",
			entries: []model.ExposureEntry{
				freshnessEntry(1, "A", 5, constants.QualityFlagVerified, periodStart),
				freshnessEntry(2, "B", 9, constants.QualityFlagVerified, periodStart),
			},
			wantFresh: false,
			wantCodes: []string{
				ReasonExposureRecordsChanged, ReasonThresholdVersionChanged,
			},
			wantDelta:       9,
			wantChangeCount: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := BuildFreshness(test.snapshot, test.snapshotDose, test.workerVersion,
				test.adminLimit, test.legalLimit, test.nearRatio, test.threshold, test.entries, now)
			if err != nil {
				t.Fatalf("BuildFreshness returned error: %v", err)
			}
			if report.IsFresh != test.wantFresh {
				t.Fatalf("IsFresh = %v, want %v; reasons=%v", report.IsFresh, test.wantFresh, report.Reasons)
			}
			if report.PeriodDoseDeltaMSV != test.wantDelta {
				t.Fatalf("PeriodDoseDeltaMSV = %v, want %v", report.PeriodDoseDeltaMSV, test.wantDelta)
			}
			if len(report.EntryChanges) != test.wantChangeCount {
				t.Fatalf("EntryChanges = %d (%+v), want %d", len(report.EntryChanges), report.EntryChanges, test.wantChangeCount)
			}
			if len(report.LimitChanges) != test.wantLimitCount {
				t.Fatalf("LimitChanges = %d (%+v), want %d", len(report.LimitChanges), report.LimitChanges, test.wantLimitCount)
			}
			if !sameCodeSet(report.ReasonCodes, test.wantCodes) {
				t.Fatalf("ReasonCodes = %v, want %v", report.ReasonCodes, test.wantCodes)
			}
			if test.wantFresh {
				if len(report.Reasons) != 0 {
					t.Fatalf("fresh report must carry no stale reasons, got %v", report.Reasons)
				}
			} else if message := report.StaleMessage(); !strings.Contains(message, "stale") {
				t.Fatalf("StaleMessage must explain staleness, got %q", message)
			}
		})
	}
}

func TestBuildFreshnessRejectsBrokenCurrentChain(t *testing.T) {
	snapshot := freshnessSnapshot()
	at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	entries := []model.ExposureEntry{
		freshnessEntry(1, "DUP", 5, constants.QualityFlagVerified, at),
		freshnessEntry(2, "DUP", 3, constants.QualityFlagVerified, at),
	}
	if _, err := BuildFreshness(snapshot, 5, 1, 12, 20, 0.9, "T1", entries, time.Now().UTC()); err == nil {
		t.Fatal("expected replay to reject a duplicate source_ref in current entries")
	}
}

func sameCodeSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := map[string]int{}
	for _, code := range got {
		counts[code]++
	}
	for _, code := range want {
		counts[code]--
		if counts[code] < 0 {
			return false
		}
	}
	return true
}
