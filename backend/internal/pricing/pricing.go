// Package pricing works out the interest rate, repayment period and total repayable for a loan.
//
// Rules (all adjustable with environment variables, see FromEnv):
//   - Rate: BaseAnnualRate plus RiskLoading x (1 - repayment confidence), rounded to the nearest 0.5%,
//     and never above MaxAnnualRate. Lower confidence from the model means a higher rate.
//   - Period: the shortest number of months (MinMonths..MaxMonths) in which the monthly payment stays at or
//     below MaxInstallmentShare of the customer's monthly income. Shorter means less total interest.
//   - Repayment: equal monthly instalments on a reducing balance (interest is charged on what is still owed).
//     Each instalment is rounded UP to a whole shilling, so the customer is told the exact amount to pay.
//
// These defaults are a starting point, not legal or commercial advice. Set them to match your own pricing,
// and check the disclosure rules that apply to your licence (for example CBK digital credit rules).
package pricing

import (
	"fmt"
	"math"
	"os"
	"strconv"
)

type Config struct {
	BaseAnnualRate      float64 // % per year before risk loading
	RiskLoading         float64 // extra % points added per 1.0 of default risk (1 - repayment confidence)
	MaxAnnualRate       float64 // % per year ceiling
	MinMonths           int
	MaxMonths           int
	MaxInstallmentShare float64 // monthly instalment as a share of monthly income, e.g. 0.35
}

// Terms is a priced loan. All money amounts are in KES.
type Terms struct {
	AnnualRatePct         float64
	Months                int
	MonthlyInstallmentKES float64
	TotalRepayableKES     float64
	TotalInterestKES      float64
}

// UnaffordableError means the amount can't be repaid within MaxMonths at the allowed share of income.
type UnaffordableError struct{ MaxKES float64 }

func (e *UnaffordableError) Error() string {
	return fmt.Sprintf("amount is too large to repay within the maximum period; most affordable is KES %.0f", e.MaxKES)
}

func getFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && v >= 0 {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// FromEnv reads pricing settings from the environment, falling back to the defaults shown.
func FromEnv() Config {
	c := Config{
		BaseAnnualRate:      getFloat("PRICING_BASE_ANNUAL_RATE", 14),
		RiskLoading:         getFloat("PRICING_RISK_LOADING", 20),
		MaxAnnualRate:       getFloat("PRICING_MAX_ANNUAL_RATE", 30),
		MinMonths:           getInt("PRICING_MIN_MONTHS", 1),
		MaxMonths:           getInt("PRICING_MAX_MONTHS", 24),
		MaxInstallmentShare: getFloat("PRICING_MAX_INSTALLMENT_SHARE", 0.35),
	}
	if c.MaxMonths < c.MinMonths {
		c.MaxMonths = c.MinMonths
	}
	if c.MaxInstallmentShare <= 0 || c.MaxInstallmentShare > 1 {
		c.MaxInstallmentShare = 0.35
	}
	return c
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// AnnualRate returns the yearly interest rate (in %) for a repayment confidence between 0 and 1.
func (c Config) AnnualRate(confidence float64) float64 {
	risk := 1 - clamp(confidence, 0, 1)
	rate := math.Round((c.BaseAnnualRate+c.RiskLoading*risk)*2) / 2
	return math.Min(rate, c.MaxAnnualRate)
}

// instalment is the exact (unrounded) monthly payment on a reducing balance.
func instalment(principal, annualPct float64, months int) float64 {
	r := annualPct / 1200
	if r == 0 {
		return principal / float64(months)
	}
	return principal * r / (1 - math.Pow(1+r, -float64(months)))
}

// Quote prices a loan. confidence is the model's repayment confidence (0 to 1).
func (c Config) Quote(principalKES, confidence, monthlyIncomeKES float64) (Terms, error) {
	if principalKES <= 0 || monthlyIncomeKES <= 0 {
		return Terms{}, fmt.Errorf("pricing needs a positive amount and income")
	}
	rate := c.AnnualRate(confidence)
	limit := math.Floor(monthlyIncomeKES * c.MaxInstallmentShare)
	for n := c.MinMonths; n <= c.MaxMonths; n++ {
		pay := math.Ceil(instalment(principalKES, rate, n))
		if pay <= limit {
			total := pay * float64(n)
			return Terms{AnnualRatePct: rate, Months: n, MonthlyInstallmentKES: pay,
				TotalRepayableKES: total, TotalInterestKES: total - principalKES}, nil
		}
	}
	return Terms{}, &UnaffordableError{MaxKES: c.maxPrincipal(rate, limit)}
}

// maxPrincipal is the largest loan (rounded down to KES 100) whose instalment fits the limit over MaxMonths.
func (c Config) maxPrincipal(annualPct, monthlyLimit float64) float64 {
	n := float64(c.MaxMonths)
	r := annualPct / 1200
	p := monthlyLimit * n
	if r > 0 {
		p = monthlyLimit * (1 - math.Pow(1+r, -n)) / r
	}
	return math.Floor(p/100) * 100
}
