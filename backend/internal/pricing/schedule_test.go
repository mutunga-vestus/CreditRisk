package pricing

import (
	"math"
	"testing"
	"time"
)

func TestScheduleClearsTheBalance(t *testing.T) {
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ amount, conf, income float64 }{
		{250000, 0.85, 120000}, {300000, 0.90, 150000}, {50000, 0.60, 40000}, {1200000, 0.95, 900000},
	} {
		q, err := cfg().Quote(tc.amount, tc.conf, tc.income)
		if err != nil {
			t.Fatal(err)
		}
		rows := Schedule(tc.amount, q.AnnualRatePct, q.Months, q.MonthlyInstallmentKES, start)
		if len(rows) != q.Months {
			t.Fatalf("%v: got %d rows, want %d", tc.amount, len(rows), q.Months)
		}
		var paid, principal float64
		for i, r := range rows {
			if r.PaymentKES != r.InterestKES+r.PrincipalKES {
				t.Fatalf("row %d does not add up: %+v", r.N, r)
			}
			if r.N != i+1 || r.BalanceKES < 0 {
				t.Fatalf("bad row %+v", r)
			}
			paid += r.PaymentKES
			principal += r.PrincipalKES
		}
		if last := rows[len(rows)-1]; last.BalanceKES != 0 {
			t.Fatalf("%v: final balance %v, want 0", tc.amount, last.BalanceKES)
		}
		if math.Abs(principal-tc.amount) > 0.01 {
			t.Fatalf("%v: principal repaid %v", tc.amount, principal)
		}
		if math.Abs(paid-q.TotalRepayableKES) > float64(q.Months) {
			t.Fatalf("%v: schedule total %v is far from quoted %v", tc.amount, paid, q.TotalRepayableKES)
		}
	}
}

func TestAddMonthsKeepsEndOfMonth(t *testing.T) {
	jan31 := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	if got := addMonths(jan31, 1); got.Month() != time.February || got.Day() != 28 {
		t.Fatalf("got %v", got)
	}
	if got := addMonths(jan31, 2); got.Month() != time.March || got.Day() != 31 {
		t.Fatalf("got %v", got)
	}
	if got := addMonths(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC), 2); got.Year() != 2027 || got.Month() != time.February {
		t.Fatalf("year rollover: %v", got)
	}
}
