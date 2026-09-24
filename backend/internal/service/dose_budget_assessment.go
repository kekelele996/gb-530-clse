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
		worker, err := workers.FindForUpdate(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("plan worker", err)
		}
		period, err := dosebudget.NewPeriod(worker.PeriodStart, request.PeriodEnd)
		if err != nil {
			return BadRequest("invalid_period", err.Error())
		}
		if request.PeriodEnd.After(time.Now().UTC().Add(5 * time.Minute)) {
			return BadRequest("invalid_period", "period_end cannot be in the future")
		}
		periodEntries, err := entries.PeriodEntries(worker.ID, period.Start, period.End)
		if err != nil {
			return Internal("could not load period exposure entries", err)
		}
		assessment, err := service.calculate(plan, worker, period, periodEntries, actor.ID)
		if err != nil {
			return err
		}
		assessment.PlanVersion = plan.Version + 1
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
				"plan_id": plan.ID, "period_end": period.End, "threshold_version": service.thresholdVersion,
				"risk_band": assessment.RiskBand, "requires_human_review": true,
			}, planAudit(beforePlan), map[string]any{
				"assessment_id": assessment.ID, "plan_status": plan.PermitStatus, "plan_version": plan.Version,
				"projected_dose_msv": assessment.ProjectedDoseMSV, "risk_band": assessment.RiskBand,
			}); err != nil {
			return err
		}
		response, err = service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
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
		entries := service.entries.WithDB(tx)
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
		periodStart, periodEnd, err := snapshotPeriod(assessment)
		if err != nil {
			return Internal("stored assessment snapshot is invalid", err)
		}
		periodEntries, err := entries.PeriodEntries(worker.ID, periodStart, periodEnd)
		if err != nil {
			return Internal("could not replay period exposure entries", err)
		}
		response, err = service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
		return err
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
		workers := service.workers.WithDB(tx)
		entries := service.entries.WithDB(tx)
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
		worker, err := workers.FindForUpdate(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("assessment worker", err)
		}
		periodStart, periodEnd, err := snapshotPeriod(assessment)
		if err != nil {
			return Internal("stored assessment snapshot is invalid", err)
		}
		periodEntries, err := entries.PeriodEntries(worker.ID, periodStart, periodEnd)
		if err != nil {
			return Internal("could not replay period exposure entries", err)
		}
		freshness, err := service.buildFreshness(&assessment, &worker, periodEntries)
		if err != nil {
			return Internal("could not verify assessment snapshot freshness", err)
		}
		if request.Decision == "accept" && !freshness.IsFresh {
			return Conflict("stale_snapshot", freshness.StaleMessage(), nil)
		}
		targetPlanStatus := constants.PermitStatusRejected
		targetAssessmentStatus := constants.AssessmentStatusRejected
		if request.Decision == "accept" {
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
				"snapshot_fresh": freshness.IsFresh, "freshness_reason_codes": freshness.ReasonCodes,
			}, planAudit(beforePlan), map[string]any{
				"assessment_status": targetAssessmentStatus, "plan_status": targetPlanStatus,
				"plan_version": plan.Version, "planning_acceptance_is_not_work_permit": true,
			}); err != nil {
			return err
		}
		response, err = service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
		return err
	})
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, err
	}
	return response, nil
}

// ReturnForReassessment sends a submitted assessment back to planning because its frozen
// snapshot no longer matches current exposure records or worker limits. The old snapshot
// stays traceable; the planner runs a new assessment and resubmits through the normal flow.
func (service *DoseBudgetAssessmentService) ReturnForReassessment(
	id uint,
	request dto.PlanVersionRequest,
	actor dto.Actor,
	requestID string,
) (dto.DoseBudgetAssessmentResponse, error) {
	var response dto.DoseBudgetAssessmentResponse
	err := service.db.Transaction(func(tx *gorm.DB) error {
		assessments := service.assessments.WithDB(tx)
		plans := service.plans.WithDB(tx)
		workers := service.workers.WithDB(tx)
		entries := service.entries.WithDB(tx)
		assessment, err := assessments.FindForUpdate(id)
		if err != nil {
			return MapRepositoryError("dose budget assessment", err)
		}
		if assessment.AssessmentStatus != constants.AssessmentStatusSubmitted {
			return Conflict("invalid_state", "only submitted assessments can be returned for reassessment", nil)
		}
		plan, err := plans.FindForUpdate(assessment.PlanID)
		if err != nil {
			return MapRepositoryError("assessment plan", err)
		}
		if plan.Version != request.Version {
			return Conflict("version_conflict", "plan changed before return for reassessment", nil)
		}
		if plan.PermitStatus != constants.PermitStatusPendingRPOReview {
			return Conflict("invalid_state", "plan is not pending RPO review", nil)
		}
		worker, err := workers.FindForUpdate(plan.WorkerID)
		if err != nil {
			return MapRepositoryError("assessment worker", err)
		}
		periodStart, periodEnd, err := snapshotPeriod(assessment)
		if err != nil {
			return Internal("stored assessment snapshot is invalid", err)
		}
		periodEntries, err := entries.PeriodEntries(worker.ID, periodStart, periodEnd)
		if err != nil {
			return Internal("could not replay period exposure entries", err)
		}
		freshness, err := service.buildFreshness(&assessment, &worker, periodEntries)
		if err != nil {
			return Internal("could not verify assessment snapshot freshness", err)
		}
		if freshness.IsFresh {
			return Conflict("fresh_snapshot", "assessment inputs still match the snapshot; reject the planning scenario instead of returning it", nil)
		}
		if err := assessments.TransitionStatus(id, constants.AssessmentStatusSubmitted, constants.AssessmentStatusReturned); err != nil {
			return Conflict("state_conflict", "assessment changed before return for reassessment", err)
		}
		if err := plans.Transition(plan.ID, plan.Version, constants.PermitStatusPendingRPOReview, constants.PermitStatusAssessed, map[string]any{}); err != nil {
			return Conflict("version_conflict", "plan changed before the return was committed", err)
		}
		beforePlan := plan
		plan.PermitStatus = constants.PermitStatusAssessed
		plan.Version++
		assessment.AssessmentStatus = constants.AssessmentStatusReturned
		if err := service.audit.RecordTx(tx, actor, requestID, "assessment.returned_for_reassessment", "dose_budget_assessment", auditID(id),
			map[string]any{
				"expected_plan_version": request.Version, "freshness_reason_codes": freshness.ReasonCodes,
				"period_dose_delta_msv": freshness.PeriodDoseDeltaMSV, "stale_reasons": freshness.Reasons,
			}, planAudit(beforePlan), map[string]any{
				"assessment_status": constants.AssessmentStatusReturned, "plan_status": plan.PermitStatus,
				"plan_version": plan.Version, "prior_snapshot_retained": true,
			}); err != nil {
			return err
		}
		response, err = service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
		return err
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
	periodStart, periodEnd, err := snapshotPeriod(assessment)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, Internal("stored assessment snapshot is invalid", err)
	}
	periodEntries, err := service.entries.PeriodEntries(worker.ID, periodStart, periodEnd)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, Internal("could not replay period exposure entries", err)
	}
	return service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
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
		periodStart, periodEnd, err := snapshotPeriod(assessment)
		if err != nil {
			return nil, dto.PageMeta{}, Internal("stored assessment snapshot is invalid", err)
		}
		periodEntries, err := service.entries.PeriodEntries(worker.ID, periodStart, periodEnd)
		if err != nil {
			return nil, dto.PageMeta{}, Internal("could not replay period exposure entries", err)
		}
		response, err := service.attachFreshness(assessmentResponse(assessment, plan, worker), &assessment, &worker, periodEntries)
		if err != nil {
			return nil, dto.PageMeta{}, err
		}
		responses = append(responses, response)
	}
	return responses, pageMeta(page, pageSize, total), nil
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

func snapshotPeriod(assessment model.DoseBudgetAssessment) (time.Time, time.Time, error) {
	snapshot := dosebudget.Snapshot{}
	if err := json.Unmarshal([]byte(assessment.InputSnapshotJSON), &snapshot); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if snapshot.PeriodStart.IsZero() || snapshot.PeriodEnd.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("snapshot is missing the assessment period")
	}
	return snapshot.PeriodStart.UTC(), snapshot.PeriodEnd.UTC(), nil
}

// buildFreshness replays the frozen snapshot period against current storage state.
func (service *DoseBudgetAssessmentService) buildFreshness(
	assessment *model.DoseBudgetAssessment,
	worker *model.WorkerProfile,
	entries []model.ExposureEntry,
) (dosebudget.FreshnessReport, error) {
	snapshot := dosebudget.Snapshot{}
	if err := json.Unmarshal([]byte(assessment.InputSnapshotJSON), &snapshot); err != nil {
		return dosebudget.FreshnessReport{}, fmt.Errorf("decode assessment snapshot: %w", err)
	}
	report, err := dosebudget.BuildFreshness(snapshot, assessment.PeriodDoseMSV, worker.Version,
		worker.AdministrativeLimitMSV, worker.AnnualLimitMSV, service.nearRatio, service.thresholdVersion,
		entries, time.Now().UTC())
	if err != nil {
		return dosebudget.FreshnessReport{}, err
	}
	return report, nil
}

func (service *DoseBudgetAssessmentService) attachFreshness(
	response dto.DoseBudgetAssessmentResponse,
	assessment *model.DoseBudgetAssessment,
	worker *model.WorkerProfile,
	entries []model.ExposureEntry,
) (dto.DoseBudgetAssessmentResponse, error) {
	report, err := service.buildFreshness(assessment, worker, entries)
	if err != nil {
		return dto.DoseBudgetAssessmentResponse{}, Internal("could not evaluate assessment freshness", err)
	}
	response.Freshness = freshnessResponse(report)
	return response, nil
}

func freshnessResponse(report dosebudget.FreshnessReport) *dto.AssessmentFreshness {
	entryChanges := make([]dto.AssessmentFreshnessEntryChange, 0, len(report.EntryChanges))
	for _, change := range report.EntryChanges {
		entryChanges = append(entryChanges, dto.AssessmentFreshnessEntryChange{
			EntryID: change.EntryID, SourceRef: change.SourceRef, ChangeType: change.ChangeType,
			QualityFlag: change.QualityFlag, DoseMSV: change.DoseMSV, OccurredAt: change.OccurredAt,
			SnapshotState: change.SnapshotState, CurrentState: change.CurrentState,
		})
	}
	limitChanges := make([]dto.AssessmentFreshnessLimitChange, 0, len(report.LimitChanges))
	for _, change := range report.LimitChanges {
		limitChanges = append(limitChanges, dto.AssessmentFreshnessLimitChange{
			LimitName: change.LimitName, ChangeType: change.ChangeType,
			SnapshotMSV: change.SnapshotMSV, CurrentMSV: change.CurrentMSV,
		})
	}
	return &dto.AssessmentFreshness{
		IsFresh: report.IsFresh, CheckedAt: report.CheckedAt,
		SnapshotWorkerVersion: report.SnapshotWorkerVersion, CurrentWorkerVersion: report.CurrentWorkerVersion,
		SnapshotPeriodDoseMSV: report.SnapshotPeriodDoseMSV, CurrentPeriodDoseMSV: report.CurrentPeriodDoseMSV,
		PeriodDoseDeltaMSV: report.PeriodDoseDeltaMSV, EntryChanges: entryChanges, LimitChanges: limitChanges,
		ThresholdChanged: report.ThresholdChanged, SnapshotThreshold: report.SnapshotThreshold,
		CurrentThreshold: report.CurrentThreshold, ReasonCodes: report.ReasonCodes, Reasons: report.Reasons,
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
