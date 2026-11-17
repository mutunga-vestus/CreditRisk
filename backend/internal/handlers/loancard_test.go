package handlers

import (
	"testing"
	"time"

	"loanscore/backend/internal/statement"
	"loanscore/backend/internal/store"
)

func TestShowLoanCard(t *testing.T) {
	paid := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	repaid := &statement.Invoice{Repaid: true, Payments: []statement.Payment{{PaidAt: paid}}}
	active := &statement.Invoice{}
	before := &store.Assessment{CreatedAt: paid.Add(-48 * time.Hour)} // run while the loan was active
	after := &store.Assessment{CreatedAt: paid.Add(time.Hour)}        // run after the last payment
	loan := func(status string) *store.LoanApplication { return &store.LoanApplication{Status: status} }

	cases := []struct {
		name   string
		status string
		inv    *statement.Invoice
		latest *store.Assessment
		want   bool
	}{
		{"pending is handled by its own card", "pending", active, nil, false},
		{"approved", "approved", active, nil, true},
		{"active loan", "disbursed", active, before, true},
		{"repaid, assessment was made during the loan", "disbursed", repaid, before, true},
		{"repaid, no assessment", "disbursed", repaid, nil, true},
		{"repaid, new assessment afterwards starts again", "disbursed", repaid, after, false},
		{"rejected", "rejected", active, nil, false},
	}
	for _, c := range cases {
		if got := showLoanCard(loan(c.status), c.inv, c.latest); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
