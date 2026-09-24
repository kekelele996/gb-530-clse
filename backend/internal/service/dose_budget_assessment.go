package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"radiation-dose-budget-control/backend/internal/constants"
	"radiation-dose-budget-control/backend/internal/dosebudget"
	"radiation-dose-budget-control/backend/internal/dto"
	"radiation-dose-budget-control/backend/internal/model"
	"radiation-dose-budget-control/backend/internal/repository"
)

type DoseBudgetAssessmentService struct {
	db               *gorm.DB
	assessments      *repository.DoseBudgetAssessmentRepository
	plans            *repository.WorkPermitPlanRepository
	workers          *repository.WorkerProfileRepository
	entries          *repository.ExposureEntryRepository
	audit            *AuditService
	nearRatio        float64
	thresholdVersion string
}

func NewDoseBudgetAssessmentService(
	db *gorm.DB,
	assessments *repository.DoseBudgetAssessmentRepository,
	plans *repository.WorkPermitPlanRepository,
	workers *repository.WorkerProfileRepository,
	entries *repository.ExposureEntryRepository,
	audit *AuditService,
	nearRatio float64,
	thresholdVersion string,
) *DoseBudgetAssessmentService {
	return &DoseBudgetAssessmentService{
		db: db, assessments: assessments, plans: plans, workers: workers, entries: entries, audit: audit,
		nearRatio: nearRatio, thresholdVersion: thresholdVersion,
	}
}

func (service *DoseBudgetAssessmentService) Assess(
	request dto.CreateDoseBudgetAssessmentRequest,
	actor dto.Actor,
	requestID string,
) (dto.DoseBudgetAssessmentResponse, error) {
	var response dto.DoseBudgetAssessmentResponse
	err := service.db.Transaction(func(tx *gorm.DB) error {
		plans := service.plans.WithDB(tx)
		workers := service.workers.WithDB(tx)
		entries := service.entries.WithDB(tx)
		assessments := service.assessments.WithDB(tx)
		plan, err := plans.FindForUpdate(request.PlanID)
		if err != nil {
			return MapRepositoryError("work permit plan", err)
		}
		if plan.Version != request.Version {
			return Conflict("version_conflict", "plan changed before assessment; refresh and recalculate", nil)
		}
		if plan.PermitStatus != constants.PermitStatusDraft && plan.PermitStatus != constants.PermitStatusAssessed {
			return Conflict("invalid_state", "only draft or assessed plans can be assessed", nil)
		}
		assessment, err := service.buildAssessment(tx, workers, entries, plan, request.PeriodEnd, actor.ID)
		if err != nil {
			return err
		}
		if err := assessments.Create(&assessment); err != nil {
			return Internal("could not persist immutable assessment", err)
		}
		if err := plans.Transition(plan.ID, plan.Version, plan.PermitStatus, constants.PermitStatusAssessed, map[string]any{}); err != nil {
			return Conflict("version_conflict", "plan changed while assessment was being stored", err)
		}
		beforePlan := plan
		plan.PermitStatus = constants.PermitStatusAssessed
		plan.Version++
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.calculated", "dose_budget_assessment", auditID(assessment.ID),
			map[string]any{
				"plan_id": plan.ID, "period_end": request.PeriodEnd, "threshold_version": service.thresholdVersion,
				"risk_band": assessment.RiskBand, "requires_human_review": true,
			}, planAudit(beforePlan), map[string]any{
				"assessment_id": assessment.ID, "plan_status": plan.PermitStatus, "plan_version": plan.Version,
				"projected_dose_msv": assessment.ProjectedDoseMSV, "risk_band": assessment.RiskBand,
			}); err != nil {
			return err
		}
		worker, err := workers.Find(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("plan worker", err)
		}
		response = assessmentResponse(assessment, plan, worker)
		freshness, err := service.freshness(tx, assessment)
		if err != nil {
			return err
		}
		report := dto.FreshnessReportFrom(freshness)
		response.Freshness = &report
		return nil
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
}

// Reassess supersedes an assessment currently awaiting RPO review with a fresh
// immutable assessment. It exists for the staleness workflow: when exposure
// records or worker limits drift while an assessment is in the review queue,
// the planner replays the current inputs instead of forcing acceptance on a
// stale snapshot. The superseded assessment is never deleted or rewritten.
func (service *DoseBudgetAssessmentService) Reassess(
	id uint,
	request dto.ReassessDoseBudgetRequest,
	actor dto.Actor,
	requestID string,
) (dto.DoseBudgetAssessmentResponse, error) {
	var response dto.DoseBudgetAssessmentResponse
	err := service.db.Transaction(func(tx *gorm.DB) error {
		plans := service.plans.WithDB(tx)
		workers := service.workers.WithDB(tx)
		entries := service.entries.WithDB(tx)
		assessments := service.assessments.WithDB(tx)
		previous, err := assessments.FindForUpdate(id)
		if err != nil {
			return MapRepositoryError("dose budget assessment", err)
		}
		if previous.AssessmentStatus != constants.AssessmentStatusSubmitted {
			return Conflict("invalid_state", "only assessments awaiting RPO review can be superseded by a reassessment", nil)
		}
		latest, err := assessments.LatestForPlan(previous.PlanID)
		if err != nil || latest.ID != previous.ID {
			return Conflict("stale_assessment", "only the latest assessment can be superseded", err)
		}
		plan, err := plans.FindForUpdate(previous.PlanID)
		if err != nil {
			return MapRepositoryError("assessment plan", err)
		}
		if plan.PermitStatus != constants.PermitStatusPendingRPOReview {
			return Conflict("invalid_state", "assessment plan is not pending RPO review", nil)
		}
		if plan.Version != request.Version {
			return Conflict("version_conflict", "plan changed before reassessment; refresh and recalculate", nil)
		}
		assessment, err := service.buildAssessment(tx, workers, entries, plan, request.PeriodEnd, actor.ID)
		if err != nil {
			return err
		}
		if err := assessments.Create(&assessment); err != nil {
			return Internal("could not persist immutable reassessment", err)
		}
		if err := assessments.TransitionStatus(previous.ID, constants.AssessmentStatusSubmitted, constants.AssessmentStatusSuperseded); err != nil {
			return Conflict("state_conflict", "assessment changed before it could be superseded", err)
		}
		if err := plans.Transition(plan.ID, plan.Version, constants.PermitStatusPendingRPOReview, constants.PermitStatusAssessed, map[string]any{}); err != nil {
			return Conflict("version_conflict", "plan changed while the reassessment was being stored", err)
		}
		beforePlan := plan
		previous.AssessmentStatus = constants.AssessmentStatusSuperseded
		plan.PermitStatus = constants.PermitStatusAssessed
		plan.Version++
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.superseded", "dose_budget_assessment", auditID(previous.ID),
			map[string]any{"superseded_by_assessment_id": assessment.ID, "plan_id": plan.ID, "reason": "freshness_reassessment"},
			assessmentAudit(previous), assessmentAudit(assessment)); err != nil {
			return err
		}
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.calculated", "dose_budget_assessment", auditID(assessment.ID),
			map[string]any{
				"plan_id": plan.ID, "period_end": request.PeriodEnd, "threshold_version": service.thresholdVersion,
				"risk_band": assessment.RiskBand, "requires_human_review": true, "supersedes_assessment_id": previous.ID,
			}, planAudit(beforePlan), map[string]any{
				"assessment_id": assessment.ID, "plan_status": plan.PermitStatus, "plan_version": plan.Version,
				"projected_dose_msv": assessment.ProjectedDoseMSV, "risk_band": assessment.RiskBand,
			}); err != nil {
			return err
		}
		worker, err := workers.Find(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("plan worker", err)
		}
		response = assessmentResponse(assessment, plan, worker)
		freshness, err := service.freshness(tx, assessment)
		if err != nil {
			return err
		}
		report := dto.FreshnessReportFrom(freshness)
		response.Freshness = &report
		return nil
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
}

// buildAssessment loads the worker and period entries under the current
// transaction and calculates a new immutable assessment from current inputs.
func (service *DoseBudgetAssessmentService) buildAssessment(
	tx *gorm.DB,
	workers *repository.WorkerProfileRepository,
	entries *repository.ExposureEntryRepository,
	plan model.WorkPermitPlan,
	periodEnd time.Time,
	createdBy uint,
) (model.DoseBudgetAssessment, error) {
	worker, err := workers.FindForUpdate(plan.WorkerID)
	if err != nil {
		return model.DoseBudgetAssessment{}, MapRepositoryError("plan worker", err)
	}
	period, err := dosebudget.NewPeriod(worker.PeriodStart, periodEnd)
	if err != nil {
		return model.DoseBudgetAssessment{}, BadRequest("invalid_period", err.Error())
	}
	if periodEnd.After(time.Now().UTC().Add(5 * time.Minute)) {
		return model.DoseBudgetAssessment{}, BadRequest("invalid_period", "period_end cannot be in the future")
	}
	periodEntries, err := entries.PeriodEntries(worker.ID, period.Start, period.End)
	if err != nil {
		return model.DoseBudgetAssessment{}, Internal("could not load period exposure entries", err)
	}
	assessment, err := service.calculate(plan, worker, period, periodEntries, createdBy)
	if err != nil {
		return model.DoseBudgetAssessment{}, err
	}
	assessment.PlanVersion = plan.Version + 1
	return assessment, nil
}

func (service *DoseBudgetAssessmentService) Compare(
	request dto.CompareDoseBudgetRequest,
) (dto.ScenarioComparisonResponse, error) {
	seen := map[uint]bool{}
	var worker model.WorkerProfile
	responses := make([]dto.DoseBudgetAssessmentResponse, 0, len(request.PlanIDs))
	highest := constants.DoseBandWithinAdmin
	for _, planID := range request.PlanIDs {
		if seen[planID] {
			return dto.ScenarioComparisonResponse{}, BadRequest("duplicate_plan", "plan_ids must be unique")
		}
		seen[planID] = true
		plan, err := service.plans.Find(planID)
		if err != nil {
			return dto.ScenarioComparisonResponse{}, MapRepositoryError("work permit plan", err)
		}
		candidateWorker, err := service.workers.Find(plan.WorkerID)
		if err != nil {
			return dto.ScenarioComparisonResponse{}, MapRepositoryError("plan worker", err)
		}
		if worker.ID == 0 {
			worker = candidateWorker
		} else if worker.ID != candidateWorker.ID {
			return dto.ScenarioComparisonResponse{}, BadRequest("mixed_workers", "scenario comparison requires plans for one worker")
		}
		period, err := dosebudget.NewPeriod(worker.PeriodStart, request.PeriodEnd)
		if err != nil {
			return dto.ScenarioComparisonResponse{}, BadRequest("invalid_period", err.Error())
		}
		entries, err := service.entries.PeriodEntries(worker.ID, period.Start, period.End)
		if err != nil {
			return dto.ScenarioComparisonResponse{}, Internal("could not load period exposure entries", err)
		}
		assessment, err := service.calculate(plan, worker, period, entries, 0)
		if err != nil {
			return dto.ScenarioComparisonResponse{}, err
		}
		response := assessmentResponse(assessment, plan, worker)
		responses = append(responses, response)
		if dosebudget.RiskRank(response.RiskBand) > dosebudget.RiskRank(highest) {
			highest = response.RiskBand
		}
	}
	return dto.ScenarioComparisonResponse{
		WorkerID: worker.ID, PeriodDoseMSV: responses[0].PeriodDoseMSV, Scenarios: responses,
		HighestRiskBand: highest, BoundaryStatement: dosebudget.BoundaryStatement,
	}, nil
}

func (service *DoseBudgetAssessmentService) Submit(
	id uint,
	request dto.PlanVersionRequest,
	actor dto.Actor,
	requestID string,
) (dto.DoseBudgetAssessmentResponse, error) {
	var response dto.DoseBudgetAssessmentResponse
	err := service.db.Transaction(func(tx *gorm.DB) error {
		assessments := service.assessments.WithDB(tx)
		plans := service.plans.WithDB(tx)
		assessment, err := assessments.FindForUpdate(id)
		if err != nil {
			return MapRepositoryError("dose budget assessment", err)
		}
		if assessment.AssessmentStatus != constants.AssessmentStatusCalculated {
			return Conflict("invalid_state", "only calculated assessments can be submitted", nil)
		}
		latest, err := assessments.LatestForPlan(assessment.PlanID)
		if err != nil || latest.ID != assessment.ID {
			return Conflict("stale_assessment", "only the latest immutable assessment can be submitted", err)
		}
		plan, err := plans.FindForUpdate(assessment.PlanID)
		if err != nil {
			return MapRepositoryError("assessment plan", err)
		}
		if plan.Version != request.Version || plan.Version != assessment.PlanVersion {
			return Conflict("version_conflict", "plan inputs changed after assessment", nil)
		}
		if plan.PermitStatus != constants.PermitStatusAssessed {
			return Conflict("invalid_state", "assessment plan is not in assessed state", nil)
		}
		if err := assessments.TransitionStatus(id, constants.AssessmentStatusCalculated, constants.AssessmentStatusSubmitted); err != nil {
			return Conflict("state_conflict", "assessment changed before submission", err)
		}
		if err := plans.Transition(plan.ID, plan.Version, constants.PermitStatusAssessed, constants.PermitStatusPendingRPOReview, map[string]any{}); err != nil {
			return Conflict("version_conflict", "plan changed before RPO submission", err)
		}
		beforePlan := plan
		plan.PermitStatus = constants.PermitStatusPendingRPOReview
		plan.Version++
		assessment.AssessmentStatus = constants.AssessmentStatusSubmitted
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.submitted", "dose_budget_assessment", auditID(id),
			map[string]any{"expected_plan_version": request.Version, "destination": "rpo_human_review"},
			planAudit(beforePlan), planAudit(plan)); err != nil {
			return err
		}
		worker, err := service.workers.WithDB(tx).Find(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("assessment worker", err)
		}
		response = assessmentResponse(assessment, plan, worker)
		freshness, err := service.freshness(tx, assessment)
		if err != nil {
			return err
		}
		report := dto.FreshnessReportFrom(freshness)
		response.Freshness = &report
		return nil
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
}

func (service *DoseBudgetAssessmentService) Review(
	id uint,
	request dto.AssessmentReviewRequest,
	actor dto.Actor,
	requestID string,
) (dto.DoseBudgetAssessmentResponse, error) {
	var response dto.DoseBudgetAssessmentResponse
	err := service.db.Transaction(func(tx *gorm.DB) error {
		assessments := service.assessments.WithDB(tx)
		plans := service.plans.WithDB(tx)
		assessment, err := assessments.FindForUpdate(id)
		if err != nil {
			return MapRepositoryError("dose budget assessment", err)
		}
		if assessment.AssessmentStatus != constants.AssessmentStatusSubmitted {
			return Conflict("invalid_state", "only submitted assessments can be reviewed", nil)
		}
		plan, err := plans.FindForUpdate(assessment.PlanID)
		if err != nil {
			return MapRepositoryError("assessment plan", err)
		}
		if plan.Version != request.Version {
			return Conflict("version_conflict", "plan changed before RPO review", nil)
		}
		if plan.PermitStatus != constants.PermitStatusPendingRPOReview {
			return Conflict("invalid_state", "plan is not pending RPO review", nil)
		}
		targetPlanStatus := constants.PermitStatusRejected
		targetAssessmentStatus := constants.AssessmentStatusRejected
		if request.Decision == "accept" {
			// Acceptance can only rest on evidence that still matches the current
			// ledger and limits. Lock the worker so the replay cannot interleave
			// with a concurrent limit adjustment. Rejecting a stale scenario stays
			// available so the planner is never blocked from producing a fresh assessment.
			if _, err := service.workers.WithDB(tx).FindForUpdate(assessment.WorkerID); err != nil {
				return MapRepositoryError("assessment worker", err)
			}
			freshness, err := service.freshness(tx, assessment)
			if err != nil {
				return err
			}
			if !freshness.Fresh {
				return ConflictDetails("stale_assessment_inputs",
					"assessment inputs changed after submission; a fresh assessment is required before acceptance",
					map[string]any{
						"stale_reason_code": freshness.StaleReasonCode, "stale_reason": freshness.StaleReason,
						"period_dose_delta_msv":    freshness.PeriodDoseDeltaMSV,
						"snapshot_period_dose_msv": freshness.SnapshotPeriodDoseMSV,
						"current_period_dose_msv":  freshness.CurrentPeriodDoseMSV,
						"exposure_change_count":    len(freshness.ExposureChanges),
						"worker_change_count":      len(freshness.WorkerChanges),
						"exposure_changes":         freshness.ExposureChanges,
						"worker_changes":           freshness.WorkerChanges,
					})
			}
			targetPlanStatus = constants.PermitStatusPlanningAccepted
			targetAssessmentStatus = constants.AssessmentStatusAccepted
		}
		if !constants.CanTransitionPermit(plan.PermitStatus, targetPlanStatus) {
			return Conflict("invalid_state", "requested review transition is not allowed", nil)
		}
		now := time.Now().UTC()
		if err := assessments.Review(id, constants.AssessmentStatusSubmitted, targetAssessmentStatus, actor.ID, now, strings.TrimSpace(request.Note)); err != nil {
			return Conflict("state_conflict", "assessment changed before review", err)
		}
		if err := plans.Transition(plan.ID, plan.Version, constants.PermitStatusPendingRPOReview, targetPlanStatus,
			map[string]any{"reviewer_id": actor.ID, "review_note": strings.TrimSpace(request.Note)}); err != nil {
			return Conflict("version_conflict", "plan changed before review was committed", err)
		}
		beforePlan := plan
		plan.PermitStatus = targetPlanStatus
		plan.Version++
		plan.ReviewerID = &actor.ID
		plan.ReviewNote = strings.TrimSpace(request.Note)
		assessment.AssessmentStatus = targetAssessmentStatus
		assessment.ReviewedBy = &actor.ID
		assessment.ReviewedAt = &now
		assessment.ReviewNote = strings.TrimSpace(request.Note)
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.reviewed", "dose_budget_assessment", auditID(id),
			map[string]any{
				"decision": request.Decision, "expected_plan_version": request.Version,
				"review_note_length": len(strings.TrimSpace(request.Note)), "planning_only": true,
			}, planAudit(beforePlan), map[string]any{
				"assessment_status": targetAssessmentStatus, "plan_status": targetPlanStatus,
				"plan_version": plan.Version, "planning_acceptance_is_not_work_permit": true,
			}); err != nil {
			return err
		}
		worker, err := service.workers.WithDB(tx).Find(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("assessment worker", err)
		}
		response = assessmentResponse(assessment, plan, worker)
		freshness, err := service.freshness(tx, assessment)
		if err != nil {
			return err
		}
		report := dto.FreshnessReportFrom(freshness)
		response.Freshness = &report
		return nil
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
}

func (service *DoseBudgetAssessmentService) Get(id uint) (dto.DoseBudgetAssessmentResponse, error) {
	assessment, err := service.assessments.Find(id)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, MapRepositoryError("dose budget assessment", err)
	}
	plan, err := service.plans.Find(assessment.PlanID)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, MapRepositoryError("assessment plan", err)
	}
	worker, err := service.workers.Find(assessment.WorkerID)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, MapRepositoryError("assessment worker", err)
	}
	response := assessmentResponse(assessment, plan, worker)
	freshness, err := service.freshness(service.db, assessment)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	report := dto.FreshnessReportFrom(freshness)
	response.Freshness = &report
	return response, nil
}

func (service *DoseBudgetAssessmentService) List(
	page, pageSize int,
	workerFilter, planFilter, status string,
) ([]dto.DoseBudgetAssessmentResponse, dto.PageMeta, error) {
	workerID, err := parseUintFilter(workerFilter)
	if err != nil {
		return nil, dto.PageMeta{}, err
	}
	planID, err := parseUintFilter(planFilter)
	if err != nil {
		return nil, dto.PageMeta{}, err
	}
	assessments, total, err := service.assessments.List(page, pageSize, workerID, planID, status)
	if err != nil {
		return nil, dto.PageMeta{}, Internal("could not list dose budget assessments", err)
	}
	responses := make([]dto.DoseBudgetAssessmentResponse, 0, len(assessments))
	for _, assessment := range assessments {
		plan, err := service.plans.Find(assessment.PlanID)
		if err != nil {
			return nil, dto.PageMeta{}, MapRepositoryError("assessment plan", err)
		}
		worker, err := service.workers.Find(assessment.WorkerID)
		if err != nil {
			return nil, dto.PageMeta{}, MapRepositoryError("assessment worker", err)
		}
		response := assessmentResponse(assessment, plan, worker)
		freshness, err := service.freshness(service.db, assessment)
		if err != nil {
			return nil, dto.PageMeta{}, err
		}
		report := dto.FreshnessReportFrom(freshness)
		response.Freshness = &report
		responses = append(responses, response)
	}
	return responses, pageMeta(page, pageSize, total), nil
}

// freshness replays a stored assessment snapshot against the current worker
// profile and exposure ledger. It is read-only: the immutable assessment row
// is never rewritten, so old snapshots stay fully reproducible.
func (service *DoseBudgetAssessmentService) freshness(handle *gorm.DB, assessment model.DoseBudgetAssessment) (dosebudget.Freshness, error) {
	var snapshot dosebudget.Snapshot
	if err := json.Unmarshal([]byte(assessment.InputSnapshotJSON), &snapshot); err != nil {
		return dosebudget.Freshness{}, Internal("stored assessment snapshot is invalid", err)
	}
	worker, err := service.workers.WithDB(handle).Find(assessment.WorkerID)
	if err != nil {
		return dosebudget.Freshness{}, MapRepositoryError("assessment worker", err)
	}
	entries, err := service.entries.WithDB(handle).PeriodEntries(worker.ID, snapshot.PeriodStart, snapshot.PeriodEnd)
	if err != nil {
		return dosebudget.Freshness{}, Internal("could not load period exposure entries", err)
	}
	likes := make([]dosebudget.ExposureEntryLike, 0, len(entries))
	for _, entry := range entries {
		likes = append(likes, dosebudget.ExposureEntryLike{
			ID: entry.ID, SourceRef: entry.SourceRef, OccurredAt: entry.OccurredAt, DoseMSV: entry.DoseMSV,
			EntryType: entry.EntryType, QualityFlag: entry.QualityFlag, CorrectionOfID: entry.CorrectionOfID,
		})
	}
	return dosebudget.EvaluateFreshness(
		dosebudget.FreshnessSnapshotInputs{
			PeriodStart: snapshot.PeriodStart, PeriodEnd: snapshot.PeriodEnd, PeriodDoseMSV: assessment.PeriodDoseMSV,
			AdminLimitMSV: snapshot.AdministrativeLimitMSV, LegalLimitMSV: snapshot.LegalLimitMSV,
			WorkerVersion: snapshot.WorkerVersion, IncludedExposureIDs: snapshot.IncludedExposureIDs,
			ExcludedExposureIDs: snapshot.ExcludedExposureIDs,
		},
		likes, worker.AdministrativeLimitMSV, worker.AnnualLimitMSV, worker.Version, time.Now().UTC(),
	), nil
}

func (service *DoseBudgetAssessmentService) calculate(
	plan model.WorkPermitPlan,
	worker model.WorkerProfile,
	period dosebudget.Period,
	entries []model.ExposureEntry,
	createdBy uint,
) (model.DoseBudgetAssessment, error) {
	summary, err := dosebudget.SummarizeEntries(entries)
	if err != nil {
		return model.DoseBudgetAssessment{}, BadRequest("invalid_exposure_chain", err.Error())
	}
	projection, err := dosebudget.CalculateProjection(summary.DoseMSV, plan.EstimatedRateMSVH, plan.PlannedMinutes)
	if err != nil {
		return model.DoseBudgetAssessment{}, BadRequest("invalid_projection", err.Error())
	}
	thresholds := dosebudget.Thresholds{
		AdministrativeLimitMSV: worker.AdministrativeLimitMSV, LegalLimitMSV: worker.AnnualLimitMSV,
		NearLegalRatio: service.nearRatio, Version: service.thresholdVersion,
	}
	decision, err := dosebudget.Evaluate(projection.ProjectedTotalMSV, thresholds)
	if err != nil {
		return model.DoseBudgetAssessment{}, BadRequest("invalid_thresholds", err.Error())
	}
	controls := []string{}
	if err := json.Unmarshal([]byte(plan.ControlsJSON), &controls); err != nil {
		return model.DoseBudgetAssessment{}, Internal("stored plan controls are invalid", err)
	}
	snapshot := dosebudget.Snapshot{
		WorkerID: worker.ID, WorkerCode: worker.WorkerCode, WorkerVersion: worker.Version,
		PlanID: plan.ID, PlanCode: plan.PlanCode, PlanVersion: plan.Version,
		PeriodStart: period.Start, PeriodEnd: period.End, EstimatedRateMSVH: plan.EstimatedRateMSVH,
		PlannedMinutes: plan.PlannedMinutes, Controls: controls,
		AdministrativeLimitMSV: worker.AdministrativeLimitMSV, LegalLimitMSV: worker.AnnualLimitMSV,
		NearLegalRatio: service.nearRatio, ThresholdVersion: service.thresholdVersion,
	}
	snapshotJSON, evidenceJSON, err := dosebudget.BuildArtifacts(snapshot, summary, decision)
	if err != nil {
		return model.DoseBudgetAssessment{}, Internal("could not build assessment evidence", err)
	}
	return model.DoseBudgetAssessment{
		WorkerID: worker.ID, PlanID: plan.ID, AssessmentStatus: constants.AssessmentStatusCalculated,
		InputSnapshotJSON: snapshotJSON, PeriodDoseMSV: projection.CurrentDoseMSV,
		ProjectedDoseMSV: projection.ProjectedTotalMSV, RemainingAdminMSV: decision.RemainingAdminMSV,
		RemainingLegalMSV: decision.RemainingLegalMSV, RiskBand: decision.RiskBand,
		EvidenceJSON: evidenceJSON, ThresholdVersion: service.thresholdVersion,
		PlanVersion: plan.Version, WorkerVersion: worker.Version, CreatedBy: createdBy,
	}, nil
}

func assessmentResponse(
	assessment model.DoseBudgetAssessment,
	plan model.WorkPermitPlan,
	worker model.WorkerProfile,
) dto.DoseBudgetAssessmentResponse {
	snapshot := map[string]interface{}{}
	_ = json.Unmarshal([]byte(assessment.InputSnapshotJSON), &snapshot)
	evidence := dto.DoseEvidence{}
	_ = json.Unmarshal([]byte(assessment.EvidenceJSON), &evidence)
	return dto.DoseBudgetAssessmentResponse{
		ID: assessment.ID, WorkerID: assessment.WorkerID, WorkerCode: worker.WorkerCode, WorkerName: worker.DisplayName,
		PlanID: assessment.PlanID, PlanCode: plan.PlanCode, AssessmentStatus: assessment.AssessmentStatus,
		InputSnapshot: snapshot, PeriodDoseMSV: assessment.PeriodDoseMSV, ProjectedDoseMSV: assessment.ProjectedDoseMSV,
		RemainingAdminMSV: assessment.RemainingAdminMSV, RemainingLegalMSV: assessment.RemainingLegalMSV,
		RiskBand: assessment.RiskBand, Evidence: evidence, ThresholdVersion: assessment.ThresholdVersion,
		PlanVersion: plan.Version, WorkerVersion: assessment.WorkerVersion, CreatedAt: assessment.CreatedAt,
		ReviewedBy: assessment.ReviewedBy, ReviewedAt: assessment.ReviewedAt, ReviewNote: assessment.ReviewNote,
	}
}

func assessmentAudit(assessment model.DoseBudgetAssessment) map[string]any {
	return map[string]any{
		"id": assessment.ID, "worker_id": assessment.WorkerID, "plan_id": assessment.PlanID,
		"assessment_status": assessment.AssessmentStatus, "period_dose_msv": assessment.PeriodDoseMSV,
		"projected_dose_msv": assessment.ProjectedDoseMSV, "remaining_admin_msv": assessment.RemainingAdminMSV,
		"remaining_legal_msv": assessment.RemainingLegalMSV, "risk_band": assessment.RiskBand,
		"threshold_version": assessment.ThresholdVersion, "plan_version": assessment.PlanVersion,
	}
}

func ensureNoAutomaticPermission(response dto.DoseBudgetAssessmentResponse) error {
	encoded, _ := json.Marshal(response)
	lower := strings.ToLower(string(encoded))
	for _, prohibited := range []string{"work_authorized", "medical_clearance", "automatic_permit"} {
		if strings.Contains(lower, prohibited) {
			return fmt.Errorf("prohibited authorization field %q", prohibited)
		}
	}
	return nil
}
