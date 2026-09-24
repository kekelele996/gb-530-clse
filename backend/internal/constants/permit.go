package constants

const (
	PermitStatusDraft            = "draft"
	PermitStatusAssessed         = "assessed"
	PermitStatusPendingRPOReview = "pending_rpo_review"
	PermitStatusPlanningAccepted = "planning_accepted"
	PermitStatusRejected         = "rejected"
	PermitStatusArchived         = "archived"
)

var PermitStatuses = []string{
	PermitStatusDraft,
	PermitStatusAssessed,
	PermitStatusPendingRPOReview,
	PermitStatusPlanningAccepted,
	PermitStatusRejected,
	PermitStatusArchived,
}

func IsPermitStatus(value string) bool {
	for _, candidate := range PermitStatuses {
		if value == candidate {
			return true
		}
	}
	return false
}

func CanTransitionPermit(from, to string) bool {
	allowed := map[string]map[string]bool{
		PermitStatusDraft:    {PermitStatusAssessed: true},
		PermitStatusAssessed: {PermitStatusAssessed: true, PermitStatusPendingRPOReview: true},
		// A fresh assessment that supersedes a stale review queue entry returns
		// the plan to assessed; the immutable superseded assessment stays traceable.
		PermitStatusPendingRPOReview: {PermitStatusPlanningAccepted: true, PermitStatusRejected: true, PermitStatusAssessed: true},
		PermitStatusPlanningAccepted: {PermitStatusArchived: true},
		PermitStatusRejected:         {PermitStatusArchived: true},
	}
	return allowed[from][to]
}

const (
	ProfileStatusActive    = "active"
	ProfileStatusSuspended = "suspended"
	ProfileStatusArchived  = "archived"
)

func IsProfileStatus(value string) bool {
	return value == ProfileStatusActive || value == ProfileStatusSuspended || value == ProfileStatusArchived
}
