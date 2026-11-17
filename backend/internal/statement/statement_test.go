package statement

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sample() Input {
	return Input{ID: 12, Status: "pending", CreatedAt: time.Date(2026, 11, 15, 22, 30, 0, 0, time.UTC),
		AmountKES: 250000, AnnualRatePct: 16, Months: 24, InstallmentKES: 12240,
		CustomerName: "Wanjiru Kamau", CustomerEmail: "w@example.com", Issuer: "Kopa Check"}
}

func active() Input {
	in := sample()
	d := time.Date(2026, 11, 20, 9, 0, 0, 0, eat)
	in.Status, in.DisbursedAt = "disbursed", &d
	in.Now = time.Date(2027, 3, 1, 12, 0, 0, 0, eat) // instalments due 20 Dec, 20 Jan, 20 Feb are in the past
	return in
}

func TestBuildAndPDF(t *testing.T) {
	inv := Build(sample())
	if inv == nil {
		t.Fatal("expected an invoice")
	}
	if inv.Number != "INV-000012" || inv.LoanRef != "LN-000012" {
		t.Fatalf("bad numbers: %s %s", inv.Number, inv.LoanRef)
	}
	if inv.IssuedAt.Day() != 16 { // 22:30 UTC on 15 Nov is already 16 Nov in Nairobi
		t.Fatalf("issued date %v is not in Nairobi time", inv.IssuedAt)
	}
	if last := inv.Rows[len(inv.Rows)-1]; last.BalanceKES != 0 {
		t.Fatalf("final balance %v", last.BalanceKES)
	}
	for _, r := range inv.Rows {
		if r.Status != StatusScheduled {
			t.Fatalf("before payout every row should be Scheduled, got %q", r.Status)
		}
	}
	if inv.NextDue != nil || inv.Repaid {
		t.Fatal("nothing is due before payout")
	}
	pdf, err := inv.PDF()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) < 2000 {
		t.Fatalf("not a usable PDF (%d bytes)", len(pdf))
	}
}

func TestDatesRunFromPayout(t *testing.T) {
	inv := Build(active())
	if first := inv.Rows[0].Due; first.Month() != time.December || first.Day() != 20 {
		t.Fatalf("first due %v, want 20 Dec", first)
	}
}

func TestPaymentsFillOldestInstalmentFirst(t *testing.T) {
	in := active()
	pay := in.InstallmentKES
	in.Payments = []PaymentIn{
		{ID: 1, AmountKES: pay, PaidAt: time.Date(2026, 12, 19, 0, 0, 0, 0, eat), Method: "mpesa", Reference: "ABC1"},
		{ID: 2, AmountKES: pay / 2, PaidAt: time.Date(2027, 1, 19, 0, 0, 0, 0, eat), Method: "mpesa", Reference: "ABC2"},
	}
	inv := Build(in)
	if inv.Rows[0].Status != StatusPaid {
		t.Fatalf("row 1: %q", inv.Rows[0].Status)
	}
	if inv.Rows[1].Status != StatusOverdue { // half paid, but due 20 Jan has passed
		t.Fatalf("row 2: %q", inv.Rows[1].Status)
	}
	if inv.Rows[2].Status != StatusOverdue || inv.Rows[3].Status != StatusUpcoming && inv.Rows[3].Status != StatusNext {
		t.Fatalf("rows 3/4: %q %q", inv.Rows[2].Status, inv.Rows[3].Status)
	}
	if inv.NextDue == nil || !inv.NextDue.Overdue || inv.NextDue.AmountKES != pay/2 {
		t.Fatalf("next due: %+v", inv.NextDue)
	}
	wantOverdue := pay/2 + pay // row 2 remainder + row 3
	if inv.OverdueKES != wantOverdue {
		t.Fatalf("overdue %v, want %v", inv.OverdueKES, wantOverdue)
	}
	if inv.PaidKES != pay*1.5 || inv.OutstandingKES != inv.TotalRepayableKES-pay*1.5 {
		t.Fatalf("paid %v outstanding %v", inv.PaidKES, inv.OutstandingKES)
	}
	if inv.Repaid {
		t.Fatal("not repaid yet")
	}
	if _, err := inv.PDF(); err != nil {
		t.Fatal(err)
	}
}

func TestFullyRepaid(t *testing.T) {
	in := active()
	probe := Build(in)
	in.Payments = []PaymentIn{{ID: 1, AmountKES: probe.TotalRepayableKES, PaidAt: in.Now, Method: "bank"}}
	inv := Build(in)
	if !inv.Repaid || inv.OutstandingKES != 0 || inv.ProgressPct != 100 || inv.NextDue != nil {
		t.Fatalf("%+v", inv)
	}
	for _, r := range inv.Rows {
		if r.Status != StatusPaid {
			t.Fatalf("row %d is %q", r.N, r.Status)
		}
	}
}

func TestBuildSkipsApplicationsWithoutTerms(t *testing.T) {
	in := sample()
	in.Months = 0
	if Build(in) != nil {
		t.Fatal("expected nil for an application with no terms")
	}
}

func TestMoney(t *testing.T) {
	if got := money(1234567); got != "KES 1,234,567" {
		t.Fatal(got)
	}
	if !strings.HasPrefix(money(0), "KES ") {
		t.Fatal("bad zero")
	}
}
