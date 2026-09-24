export type DoseBand = 'within_admin' | 'above_admin' | 'near_legal' | 'above_legal' | 'invalid';
export type QualityFlag = 'pending' | 'verified' | 'rejected';
export type EntryType = 'confirmed' | 'reversal' | 'replacement';
export type AssessmentStatus = 'calculated' | 'submitted' | 'returned' | 'accepted' | 'rejected';

export interface ExposureEntry {
  id: number;
  worker_id: number;
  worker_code: string;
  worker_name: string;
  source_ref: string;
  occurred_at: string;
  dose_msv: number;
  entry_type: EntryType;
  quality_flag: QualityFlag;
  verified_by?: number;
  verified_at?: string;
  correction_of_id?: number;
  note: string;
  created_at: string;
}

export interface ExposureInput {
  worker_id: number;
  source_ref: string;
  occurred_at: string;
  dose_msv: number;
  note: string;
}

export interface DoseEvidence {
  period_start: string;
  period_end: string;
  verified_entry_count: number;
  excluded_entry_count: number;
  corrected_chain_count: number;
  formula: string;
  projection_formula: string;
  administrative_limit_msv: number;
  annual_legal_limit_msv: number;
  near_legal_ratio: number;
  threshold_version: string;
  requires_manual_review: boolean;
  escalation_reason: string;
  boundary_statement: string;
}

export interface AssessmentFreshnessEntryChange {
  entry_id: number;
  source_ref: string;
  change_type: 'entry_added' | 'entry_removed' | 'entry_quality_changed';
  quality_flag: QualityFlag | '';
  dose_msv: number;
  occurred_at: string;
  snapshot_state: string;
  current_state: string;
}

export interface AssessmentFreshnessLimitChange {
  limit_name: 'administrative_limit_msv' | 'annual_legal_limit_msv';
  change_type: 'administrative_limit_changed' | 'legal_limit_changed';
  snapshot_msv: number;
  current_msv: number;
}

export interface AssessmentFreshness {
  is_fresh: boolean;
  checked_at: string;
  snapshot_worker_version: number;
  current_worker_version: number;
  snapshot_period_dose_msv: number;
  current_period_dose_msv: number;
  period_dose_delta_msv: number;
  entry_changes: AssessmentFreshnessEntryChange[];
  limit_changes: AssessmentFreshnessLimitChange[];
  threshold_changed: boolean;
  snapshot_threshold_version: string;
  current_threshold_version: string;
  reason_codes: string[];
  reasons: string[];
}

export interface DoseBudgetAssessment {
  id: number;
  worker_id: number;
  worker_code: string;
  worker_name: string;
  plan_id: number;
  plan_code: string;
  assessment_status: AssessmentStatus;
  input_snapshot: Record<string, unknown>;
  period_dose_msv: number;
  projected_dose_msv: number;
  remaining_admin_msv: number;
  remaining_legal_msv: number;
  risk_band: DoseBand;
  evidence: DoseEvidence;
  threshold_version: string;
  plan_version: number;
  worker_version: number;
  freshness?: AssessmentFreshness;
  created_at: string;
  reviewed_by?: number;
  reviewed_at?: string;
  review_note: string;
}

export interface ScenarioComparison {
  worker_id: number;
  period_dose_msv: number;
  scenarios: DoseBudgetAssessment[];
  highest_risk_band: DoseBand;
  boundary_statement: string;
}
