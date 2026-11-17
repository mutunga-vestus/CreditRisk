package pricing

import (
	"math"
	"time"
)

// Row is one line of a repayment schedule. All money amounts are whole KES.
type Row struct {
	N            int
	Due          time.Time
	PaymentKES   float64
	InterestKES  float64
	PrincipalKES float64
	BalanceKES   float64 // still owed after this payment
}

// Schedule lays out a reducing-balance repayment plan: interest is charged each month on the balance still
// owed, rounded to the nearest shilling, and the rest of the instalment pays the balance down.
//
// Every instalment is the same amount that Quote gave (rounded up to a whole shilling). So that the last
// row clears the balance exactly, its interest line absorbs the few shillings of rounding.
// If start is the zero time, the Due dates are left empty.
func Schedule(principal, annualPct float64, months int, installment float64, start time.Time) []Row {
	if principal <= 0 || months <= 0 || installment <= 0 {
		return nil
	}
	r := annualPct / 1200
	balance := principal
	rows := make([]Row, 0, months)
	for n := 1; n <= months && balance > 0; n++ {
		interest := math.Round(balance * r)
		pay := installment
		part := pay - interest
		if n == months || part > balance {
			part = balance
			if interest = pay - part; interest < 0 { // balance grew past one instalment: charge it in full
				interest = math.Round(balance * r)
				pay = part + interest
			}
		}
		balance = math.Round((balance-part)*100) / 100
		row := Row{N: n, PaymentKES: pay, InterestKES: interest, PrincipalKES: part, BalanceKES: balance}
		if !start.IsZero() {
			row.Due = addMonths(start, n)
		}
		rows = append(rows, row)
	}
	return rows
}

// addMonths moves a date forward by n months, keeping the day of the month where it exists
// (31 Jan + 1 month is the last day of February, not early March).
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, t.Location())
}
