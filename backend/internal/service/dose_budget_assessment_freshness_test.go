package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"radiation-dose-budget-control/backend/internal/constants"
	"radiation-dose-budget-control/backend/internal/dto"
	"radiation-dose-budget-control/backend/internal/model"
	"radiation-dose-budget-control/backend/internal/repository"
)

type assessmentFixture struct {
	db          *gorm.DB
	service     *DoseBudgetAssessmentService
	workers     *repository.WorkerProfileRepository
	entries     *repository.ExposureEntryRepository
	assessments *repository.DoseBudgetAssessmentRepository
	worker      model.WorkerProfile
	plan        model.WorkPermitPlan
	assessment  model.DoseBudgetAssessment
	planner     dto.Actor
	reviewer    dto.Actor
	periodEnd   time.Time
}

func newAssessmentFixture(t *testing.T) assessmentFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:assessment-freshness-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.WorkerProfile{}, &model.WorkPermitPlan{}, &model.ExposureEntry{},
		&model.DoseBudgetAssessment{}, &model.User{}, &model.AuditEvent{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	plans := repository.NewWorkPermitPlanRepository(db)
	workers := repository.NewWorkerProfileRepository(db)
	entries := repository.NewExposureEntryRepository(db)
	assessments := repository.NewDoseBudgetAssessmentRepository(db)
	system := repository.NewSystemRepository(db)
	audit := NewAuditService(system)
	svc := NewDoseBudgetAssessmentService(db, assessments, plans, workers, entries, audit, 0.9, "T-2026.1")

	periodStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	worker := model.WorkerProfile{
		WorkerCode: "W-FRESH", DisplayName: "Fresh Worker", AuthorizationLevel: "radiation worker",
		AnnualLimitMSV: 20, AdministrativeLimitMSV: 12, ProfileStatus: constants.ProfileStatusActive,
		PeriodStart: periodStart, Version: 1,
	}
	if err := workers.Create(&worker); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	controls, _ := json.Marshal([]string{"lead shielding", "dosimeter check"})
	plan := model.WorkPermitPlan{
		PlanCode: "P-FRESH", WorkerID: worker.ID, WorkArea: "Bay 1", TaskCategory: "inspection",
		EstimatedRateMSVH: 0.1, PlannedMinutes: 60, ControlsJSON: string(controls),
		PermitStatus: constants.PermitStatusDraft, Version: 1, CreatedBy: 1,
	}
	if err := plans.Create(&plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	entry := model.ExposureEntry{
		WorkerID: worker.ID, SourceRef: "SRC-FRESH-1", OccurredAt: periodStart.Add(24 * time.Hour),
		DoseMSV: 2, EntryType: constants.EntryTypeConfirmed, QualityFlag: constants.QualityFlagVerified,
		CreatedBy: 1,
	}
	if err := entries.Create(&entry); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	planner := dto.Actor{ID: 1, Username: "planner", Role: constants.RolePlanner}
	reviewer := dto.Actor{ID: 2, Username: "rpo", Role: constants.RoleRPOReviewer}
	created, err := svc.Assess(
		dto.CreateDoseBudgetAssessmentRequest{PlanID: plan.ID, PeriodEnd: periodEnd, Version: 1},
		planner, "req-assess",
	)
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if _, err := svc.Submit(created.ID, dto.PlanVersionRequest{Version: created.PlanVersion}, planner, "req-submit"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	stored, err := assessments.Find(created.ID)
	if err != nil {
		t.Fatalf("reload assessment: %v", err)
	}
	currentPlan, err := plans.Find(plan.ID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	return assessmentFixture{
		db: db, service: svc, workers: workers, entries: entries, assessments: assessments,
		worker: worker, plan: currentPlan, assessment: stored, planner: planner, reviewer: reviewer,
		periodEnd: periodEnd,
	}
}

func reviewNote() string { return "independent RPO planning disposition recorded" }

// A newly verified exposure entry after submission must block acceptance, but rejection stays possible.
func TestReviewAcceptBlockedWhenExposureRecordVerifiedAfterSubmission(t *testing.T) {
	fixture := newAssessmentFixture(t)
	newEntry := model.ExposureEntry{
		WorkerID: fixture.worker.ID, SourceRef: "SRC-FRESH-2",
		OccurredAt: fixture.worker.PeriodStart.Add(48 * time.Hour), DoseMSV: 3,
		EntryType: constants.EntryTypeConfirmed, QualityFlag: constants.QualityFlagVerified, CreatedBy: 2,
	}
	if err := fixture.entries.Create(&newEntry); err != nil {
		t.Fatalf("create late entry: %v", err)
	}

	_, err := fixture.service.Review(fixture.assessment.ID,
		dto.AssessmentReviewRequest{Decision: "accept", Note: reviewNote(), Version: fixture.plan.Version},
		fixture.reviewer, "req-review-accept")
	if err == nil {
		t.Fatal("accept on a stale snapshot must be rejected")
	}
	appErr, ok := err.(*AppError)
	if !ok || appErr.Code != "stale_snapshot" || !strings.Contains(appErr.Message, "stale") {
		t.Fatalf("expected stale_snapshot conflict with an explanation, got %v", err)
	}

	stored, _ := fixture.assessments.Find(fixture.assessment.ID)
	if stored.AssessmentStatus != constants.AssessmentStatusSubmitted {
		t.Fatalf("assessment status = %q, want submitted after blocked accept", stored.AssessmentStatus)
	}

	// Rejection is a human planning disposition and must remain available on stale evidence.
	rejected, err := fixture.service.Review(fixture.assessment.ID,
		dto.AssessmentReviewRequest{Decision: "reject", Note: reviewNote(), Version: fixture.plan.Version},
		fixture.reviewer, "req-review-reject")
	if err != nil {
		t.Fatalf("reject on stale snapshot should remain allowed: %v", err)
	}
	if rejected.AssessmentStatus != constants.AssessmentStatusRejected || rejected.Freshness == nil || rejected.Freshness.IsFresh {
		t.Fatalf("rejected response should still explain staleness: %+v", rejected.Freshness)
	}
}

// Adjusting worker limits after submission surfaces the exact limit change and blocks acceptance.
func TestReviewAcceptBlockedWhenWorkerLimitsAdjusted(t *testing.T) {
	fixture := newAssessmentFixture(t)
	updated := fixture.worker
	updated.AdministrativeLimitMSV = 10
	updated.AnnualLimitMSV = 18
	if err := fixture.workers.Update(updated, fixture.worker.Version); err != nil {
		t.Fatalf("adjust limits: %v", err)
	}

	_, err := fixture.service.Review(fixture.assessment.ID,
		dto.AssessmentReviewRequest{Decision: "accept", Note: reviewNote(), Version: fixture.plan.Version},
		fixture.reviewer, "req-review-limits")
	if err == nil {
		t.Fatal("accept must be blocked when worker limits moved")
	}
	appErr, _ := err.(*AppError)
	if appErr == nil || appErr.Code != "stale_snapshot" {
		t.Fatalf("expected stale_snapshot conflict, got %v", err)
	}

	detail, err := fixture.service.Get(fixture.assessment.ID)
	if err != nil {
		t.Fatalf("get assessment: %v", err)
	}
	if detail.Freshness == nil || detail.Freshness.IsFresh {
		t.Fatal("assessment detail must report the stale snapshot")
	}
	if len(detail.Freshness.LimitChanges) != 2 {
		t.Fatalf("LimitChanges = %+v, want both limits", detail.Freshness.LimitChanges)
	}
	if detail.Freshness.CurrentWorkerVersion != fixture.worker.Version+1 {
		t.Fatalf("CurrentWorkerVersion = %d, want %d", detail.Freshness.CurrentWorkerVersion, fixture.worker.Version+1)
	}
}

// ReturnForReassessment hands a stale submitted assessment back to planning while keeping the old snapshot.
func TestReturnForReassessmentCreatesRetracablePathToNewAssessment(t *testing.T) {
	fixture := newAssessmentFixture(t)
	late := model.ExposureEntry{
		WorkerID: fixture.worker.ID, SourceRef: "SRC-FRESH-3",
		OccurredAt: fixture.worker.PeriodStart.Add(72 * time.Hour), DoseMSV: 1.5,
		EntryType: constants.EntryTypeConfirmed, QualityFlag: constants.QualityFlagVerified, CreatedBy: 2,
	}
	if err := fixture.entries.Create(&late); err != nil {
		t.Fatalf("create late entry: %v", err)
	}

	returned, err := fixture.service.ReturnForReassessment(fixture.assessment.ID,
		dto.PlanVersionRequest{Version: fixture.plan.Version}, fixture.reviewer, "req-return")
	if err != nil {
		t.Fatalf("return for reassessment: %v", err)
	}
	if returned.AssessmentStatus != constants.AssessmentStatusReturned {
		t.Fatalf("status = %q, want returned", returned.AssessmentStatus)
	}
	plan, _ := fixture.service.plans.Find(fixture.plan.ID)
	if plan.PermitStatus != constants.PermitStatusAssessed {
		t.Fatalf("plan status = %q, want assessed", plan.PermitStatus)
	}

	// The planner reassesses current inputs: a new immutable assessment is appended, old one retained.
	newAssessment, err := fixture.service.Assess(
		dto.CreateDoseBudgetAssessmentRequest{PlanID: plan.ID, PeriodEnd: fixture.periodEnd, Version: plan.Version},
		fixture.planner, "req-reassess")
	if err != nil {
		t.Fatalf("reassess: %v", err)
	}
	if newAssessment.ID == fixture.assessment.ID {
		t.Fatal("reassessment must append a new immutable assessment")
	}
	if newAssessment.PeriodDoseMSV != 3.5 {
		t.Fatalf("new period dose = %v, want 3.5 including the late entry", newAssessment.PeriodDoseMSV)
	}
	old, err := fixture.service.Get(fixture.assessment.ID)
	if err != nil {
		t.Fatalf("old snapshot must remain traceable: %v", err)
	}
	if old.AssessmentStatus != constants.AssessmentStatusReturned || old.PeriodDoseMSV != 2 {
		t.Fatalf("old snapshot altered: status=%s dose=%v", old.AssessmentStatus, old.PeriodDoseMSV)
	}
	items, _, err := fixture.service.List(1, 50, "", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("assessment history = %d items, want old and new snapshots", len(items))
	}

	// The fresh assessment can run the unchanged submit -> accept flow.
	submitted, err := fixture.service.Submit(newAssessment.ID,
		dto.PlanVersionRequest{Version: newAssessment.PlanVersion}, fixture.planner, "req-resubmit")
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	accepted, err := fixture.service.Review(newAssessment.ID,
		dto.AssessmentReviewRequest{Decision: "accept", Note: reviewNote(), Version: submitted.PlanVersion},
		fixture.reviewer, "req-accept-fresh")
	if err != nil {
		t.Fatalf("accept fresh assessment: %v", err)
	}
	if accepted.AssessmentStatus != constants.AssessmentStatusAccepted {
		t.Fatalf("status = %q, want accepted", accepted.AssessmentStatus)
	}
}

// A fresh submitted snapshot cannot be returned; rejection remains the correct human disposition.
func TestReturnForReassessmentRejectsFreshSnapshot(t *testing.T) {
	fixture := newAssessmentFixture(t)
	_, err := fixture.service.ReturnForReassessment(fixture.assessment.ID,
		dto.PlanVersionRequest{Version: fixture.plan.Version}, fixture.reviewer, "req-return-fresh")
	if err == nil {
		t.Fatal("returning a fresh snapshot must be rejected")
	}
	appErr, _ := err.(*AppError)
	if appErr == nil || appErr.Code != "fresh_snapshot" {
		t.Fatalf("expected fresh_snapshot conflict, got %v", err)
	}
}
