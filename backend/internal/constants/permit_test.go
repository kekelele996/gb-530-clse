package constants

import "testing"

func TestPermitStatusTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{PermitStatusDraft, PermitStatusAssessed, true},
		{PermitStatusAssessed, PermitStatusPendingRPOReview, true},
		{PermitStatusPendingRPOReview, PermitStatusPlanningAccepted, true},
		{PermitStatusPendingRPOReview, PermitStatusRejected, true},
		{PermitStatusPendingRPOReview, PermitStatusAssessed, true},
		{PermitStatusPlanningAccepted, PermitStatusArchived, true},
		{PermitStatusDraft, PermitStatusPlanningAccepted, false},
		{PermitStatusPlanningAccepted, PermitStatusPendingRPOReview, false},
		{PermitStatusAssessed, PermitStatusPlanningAccepted, false},
		{PermitStatusRejected, PermitStatusAssessed, false},
	}
	for _, test := range tests {
		if got := CanTransitionPermit(test.from, test.to); got != test.want {
			t.Fatalf("CanTransitionPermit(%q, %q) = %v, want %v", test.from, test.to, got, test.want)
		}
	}
}
