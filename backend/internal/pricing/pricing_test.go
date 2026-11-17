package pricing

import (
	"errors"
	"math"
	"testing"
)

func cfg() Config {
	return Config{BaseAnnualRate: 14, RiskLoading: 20, MaxAnnualRate: 30, MinMonths: 1, MaxMonths: 24, MaxInstallmentShare: 0.35}
}

func TestInstalmentMatchesKnownValue(t *testing.T) {
	// Standard loan maths: 100,000 at 12% a year over 12 months is about 8,884.88 a month.
	got := instalment(100000, 12, 12)
	if math.Abs(got-8884.88) > 0.01 {
		t.Fatalf("instalment = %.4f, want about 8884.88", got)
	}
}

func TestZeroRate(t *testing.T) {
	if got := instalment(12000, 0, 12); got != 1000 {
		t.Fatalf("got %v, want 1000", got)
	}
}

func TestRateRisesAsConfidenceFalls(t *testing.T) {
	c := cfg()
	hi, mid, lo := c.AnnualRate(0.98), c.AnnualRate(0.80), c.AnnualRate(0.55)
	if !(hi < mid && mid < lo) {
		t.Fatalf("rates not increasing as confidence falls: %v %v %v", hi, mid, lo)
	}
	if hi != 14.5 || lo != 23 { // 14+0.4=14.4 -> 14.5 ; 14+9=23
		t.Fatalf("unexpected rates: %v %v", hi, lo)
	}
}

func TestRateIsCappedAndClamped(t *testing.T) {
	c := cfg()
	c.MaxAnnualRate = 18
	if got := c.AnnualRate(0.0); got != 18 {
		t.Fatalf("cap not applied: %v", got)
	}
	if got := c.AnnualRate(5); got != 14 { // confidence above 1 behaves like 1
		t.Fatalf("clamp failed: %v", got)
	}
}

func TestQuotePicksShortestAffordableTerm(t *testing.T) {
	c := cfg()
	// Income 150,000 -> instalment limit 52,500. 300,000 at 16.0%.
	q, err := c.Quote(300000, 0.90, 150000)
	if err != nil {
		t.Fatal(err)
	}
	if q.MonthlyInstallmentKES > 52500 {
		t.Fatalf("instalment %v exceeds limit", q.MonthlyInstallmentKES)
	}
	// One month shorter must NOT fit, otherwise the term wasn't the shortest.
	if q.Months > 1 && math.Ceil(instalment(300000, q.AnnualRatePct, q.Months-1)) <= 52500 {
		t.Fatalf("term %d is not the shortest affordable", q.Months)
	}
}

func TestQuoteTotalsAreConsistent(t *testing.T) {
	q, err := cfg().Quote(250000, 0.85, 120000)
	if err != nil {
		t.Fatal(err)
	}
	if q.TotalRepayableKES != q.MonthlyInstallmentKES*float64(q.Months) {
		t.Fatalf("total %v != %v x %d", q.TotalRepayableKES, q.MonthlyInstallmentKES, q.Months)
	}
	if math.Abs(q.TotalInterestKES-(q.TotalRepayableKES-250000)) > 1e-9 || q.TotalInterestKES < 0 {
		t.Fatalf("bad interest: %+v", q)
	}
	// Rounded-up instalments mean the customer never repays less than the exact amount.
	if q.TotalRepayableKES < instalment(250000, q.AnnualRatePct, q.Months)*float64(q.Months) {
		t.Fatalf("total below exact amount: %+v", q)
	}
}

func TestUnaffordableReturnsAWorkingMaximum(t *testing.T) {
	c := cfg()
	_, err := c.Quote(5_000_000, 0.9, 100000)
	var ue *UnaffordableError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnaffordableError, got %v", err)
	}
	if ue.MaxKES <= 0 || ue.MaxKES >= 5_000_000 {
		t.Fatalf("odd max: %v", ue.MaxKES)
	}
	// The suggested maximum must itself be quotable, and be close to the limit.
	if _, err := c.Quote(ue.MaxKES, 0.9, 100000); err != nil {
		t.Fatalf("suggested maximum %v is not affordable: %v", ue.MaxKES, err)
	}
	if _, err := c.Quote(ue.MaxKES+1000, 0.9, 100000); err == nil {
		t.Fatalf("maximum is too low: %v + 1000 should not fit", ue.MaxKES)
	}
}

func TestBadInputs(t *testing.T) {
	if _, err := cfg().Quote(0, 0.9, 100000); err == nil {
		t.Fatal("zero principal should fail")
	}
	if _, err := cfg().Quote(1000, 0.9, 0); err == nil {
		t.Fatal("zero income should fail")
	}
}
