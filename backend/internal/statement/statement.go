// Package statement builds the loan invoice a customer sees on their dashboard and downloads as a PDF:
// the loan terms, a month-by-month repayment schedule, and which instalments have been paid.
package statement

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"time"
	_ "time/tzdata" // so Nairobi time works on machines without a timezone database (such as Windows)

	"github.com/go-pdf/fpdf"

	"loanscore/backend/internal/pricing"
)

var eat = func() *time.Location {
	if l, err := time.LoadLocation("Africa/Nairobi"); err == nil {
		return l
	}
	return time.FixedZone("EAT", 3*60*60)
}()

// Location is the timezone used for every date on an invoice.
func Location() *time.Location { return eat }

var statusLabels = map[string]string{
	"pending": "Pending review", "approved": "Approved", "disbursed": "Paid out",
	"rejected": "Rejected", "cancelled": "Cancelled",
}

// MethodLabels names the ways a payment can be recorded. MethodOrder is the order to list them in.
var MethodLabels = map[string]string{
	"mpesa": "M-Pesa", "bank": "Bank transfer", "cash": "Cash", "other": "Other",
}
var MethodOrder = []string{"mpesa", "bank", "cash", "other"}

// Instalment statuses shown to the customer.
const (
	StatusPaid      = "Paid"
	StatusPart      = "Part paid"
	StatusOverdue   = "Overdue"
	StatusNext      = "Next due"
	StatusUpcoming  = "Upcoming"
	StatusScheduled = "Scheduled" // loan not paid out yet
)

type PaymentIn struct {
	ID        int64
	AmountKES float64
	PaidAt    time.Time
	Method    string
	Reference string
}

// Input is the saved loan application plus the customer it belongs to.
type Input struct {
	ID             int64
	Status         string
	CreatedAt      time.Time
	DisbursedAt    *time.Time
	AmountKES      float64
	AnnualRatePct  float64
	Months         int
	InstallmentKES float64
	CustomerName   string
	CustomerEmail  string
	Issuer         string
	Payments       []PaymentIn
	Now            time.Time // zero means the current time; tests set it
}

type Payment struct {
	ID          int64
	AmountKES   float64
	PaidAt      time.Time
	MethodLabel string
	Reference   string
}

// Line is one instalment with how much of it has been paid.
type Line struct {
	pricing.Row
	PaidKES float64
	Status  string
}

type NextPayment struct {
	Due       time.Time
	AmountKES float64 // what is still owed on that instalment
	Overdue   bool
}

type Invoice struct {
	ID                int64
	Number            string // INV-000012
	LoanRef           string // LN-000012, the same reference shown on the dashboard
	StatusLabel       string
	Issuer            string
	CustomerName      string
	CustomerEmail     string
	IssuedAt          time.Time
	Disbursed         bool
	DisbursedAt       time.Time // zero until the loan is paid out
	PrincipalKES      float64
	AnnualRatePct     float64
	Months            int
	InstallmentKES    float64
	TotalInterestKES  float64
	TotalRepayableKES float64
	PaidKES           float64
	OutstandingKES    float64
	OverdueKES        float64
	ProgressPct       int
	Repaid            bool
	NextDue           *NextPayment
	Rows              []Line
	Payments          []Payment
}

// Build returns nil when the application has no repayment terms yet (applications made before pricing existed).
//
// Payments are applied to the oldest unpaid instalment first. Repayment dates run from the payout date once
// the loan has been paid out; before that they are indicative and start from the application date.
func Build(in Input) *Invoice {
	if in.Months <= 0 || in.AmountKES <= 0 || in.InstallmentKES <= 0 {
		return nil
	}
	start := in.CreatedAt
	if in.DisbursedAt != nil {
		start = *in.DisbursedAt
	}
	rows := pricing.Schedule(in.AmountKES, in.AnnualRatePct, in.Months, in.InstallmentKES, start.In(eat))
	if len(rows) == 0 {
		return nil
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(eat)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, eat)
	disbursed := in.Status == "disbursed"

	var total float64
	for _, r := range rows {
		total += r.PaymentKES
	}
	var paid float64
	pays := make([]Payment, 0, len(in.Payments))
	for _, p := range in.Payments {
		paid += p.AmountKES
		label := MethodLabels[p.Method]
		if label == "" {
			label = p.Method
		}
		pays = append(pays, Payment{ID: p.ID, AmountKES: p.AmountKES, PaidAt: p.PaidAt.In(eat), MethodLabel: label, Reference: p.Reference})
	}

	inv := &Invoice{
		ID: in.ID, Number: fmt.Sprintf("INV-%06d", in.ID), LoanRef: fmt.Sprintf("LN-%06d", in.ID),
		StatusLabel: statusLabels[in.Status], Issuer: in.Issuer, CustomerName: in.CustomerName, CustomerEmail: in.CustomerEmail,
		IssuedAt: in.CreatedAt.In(eat), Disbursed: disbursed, PrincipalKES: in.AmountKES, AnnualRatePct: in.AnnualRatePct,
		Months: len(rows), InstallmentKES: in.InstallmentKES, TotalRepayableKES: total, TotalInterestKES: total - in.AmountKES,
		Payments: pays,
	}
	if inv.StatusLabel == "" {
		inv.StatusLabel = in.Status
	}
	if in.DisbursedAt != nil {
		inv.DisbursedAt = in.DisbursedAt.In(eat)
	}

	remaining := paid
	for _, r := range rows {
		applied := math.Min(remaining, r.PaymentKES)
		remaining -= applied
		ln := Line{Row: r, PaidKES: applied}
		unpaid := r.PaymentKES - applied
		switch {
		case !disbursed:
			ln.Status = StatusScheduled
		case unpaid < 0.5:
			ln.Status = StatusPaid
		case r.Due.Before(today):
			ln.Status = StatusOverdue
			inv.OverdueKES += unpaid
		case applied > 0:
			ln.Status = StatusPart
		default:
			ln.Status = StatusUpcoming
		}
		if disbursed && unpaid >= 0.5 && inv.NextDue == nil {
			inv.NextDue = &NextPayment{Due: r.Due, AmountKES: unpaid, Overdue: ln.Status == StatusOverdue}
			if ln.Status == StatusUpcoming {
				ln.Status = StatusNext
			}
		}
		inv.Rows = append(inv.Rows, ln)
	}

	inv.PaidKES = paid
	inv.OutstandingKES = math.Max(0, total-paid)
	inv.Repaid = disbursed && inv.OutstandingKES < 0.5
	if total > 0 {
		inv.ProgressPct = int(math.Max(0, math.Min(100, math.Round(paid/total*100))))
	}
	return inv
}

func money(v float64) string {
	s := strconv.FormatInt(int64(math.Round(v)), 10)
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return "KES " + string(out)
}

func day(t time.Time) string { return t.Format("2 Jan 2006") }

func plural(n int) string {
	if n == 1 {
		return "1 month"
	}
	return fmt.Sprintf("%d months", n)
}

func (inv *Invoice) note() string {
	dates := "Repayment dates assume the first instalment is due one month after the application date. They are confirmed when the loan is paid out."
	if inv.Disbursed {
		dates = "Repayment dates run from the day the loan was paid out."
	}
	return dates + " Interest is charged only on the balance still owed, and each instalment is rounded up to a whole shilling. " +
		"Payments are applied to the oldest unpaid instalment first. This invoice shows the repayment schedule and payments recorded so far."
}

// PDF renders the invoice as an A4 document.
func (inv *Invoice) PDF() ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetCellMargin(2.2) // breathing room between right-aligned numbers and the next column
	pdf.SetTitle(inv.Number, false)
	pdf.AddPage()

	ink := func() { pdf.SetTextColor(27, 35, 64) }
	grey := func() { pdf.SetTextColor(110, 110, 120) }
	room := func(h float64) { // start a new page if h mm will not fit
		if pdf.GetY()+h > 280 {
			pdf.AddPage()
			pdf.SetY(15)
		}
	}

	// Banner
	pdf.SetFillColor(27, 35, 64)
	pdf.Rect(0, 0, 210, 34, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetXY(15, 11)
	pdf.CellFormat(95, 9, tr(inv.Issuer), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(85, 9, "LOAN INVOICE", "", 0, "R", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetXY(110, 21)
	pdf.CellFormat(85, 5, inv.Number, "", 0, "R", false, 0, "")

	// Billed to
	pdf.SetXY(15, 44)
	pdf.SetFont("Helvetica", "B", 9)
	grey()
	pdf.CellFormat(90, 5, "BILLED TO", "", 1, "L", false, 0, "")
	pdf.SetX(15)
	pdf.SetFont("Helvetica", "B", 11)
	ink()
	pdf.CellFormat(90, 6, tr(inv.CustomerName), "", 1, "L", false, 0, "")
	pdf.SetX(15)
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(90, 5, tr(inv.CustomerEmail), "", 1, "L", false, 0, "")

	// Invoice details
	meta := [][2]string{{"Invoice no.", inv.Number}, {"Issued", day(inv.IssuedAt)}, {"Loan reference", inv.LoanRef}}
	if inv.Disbursed && !inv.DisbursedAt.IsZero() {
		meta = append(meta, [2]string{"Paid out", day(inv.DisbursedAt)})
	}
	status := inv.StatusLabel
	if inv.Repaid {
		status = "Fully repaid"
	}
	meta = append(meta, [2]string{"Status", status})
	y := 44.0
	for _, m := range meta {
		pdf.SetXY(115, y)
		pdf.SetFont("Helvetica", "", 9)
		grey()
		pdf.CellFormat(32, 5, m[0], "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "B", 10)
		ink()
		pdf.CellFormat(48, 5, m[1], "", 0, "R", false, 0, "")
		y += 6
	}

	// Loan summary: 4 columns x 2 rows
	items := [][2]string{
		{"Amount borrowed", money(inv.PrincipalKES)}, {"Interest rate", fmt.Sprintf("%.1f%% a year", inv.AnnualRatePct)},
		{"Repayment period", plural(inv.Months)}, {"Monthly instalment", money(inv.InstallmentKES)},
		{"Total interest", money(inv.TotalInterestKES)}, {"Total repayable", money(inv.TotalRepayableKES)},
		{"Paid to date", money(inv.PaidKES)}, {"Outstanding", money(inv.OutstandingKES)},
	}
	pdf.SetFillColor(247, 245, 240)
	pdf.Rect(15, 80, 180, 28, "F")
	for i, it := range items {
		x := 19 + float64(i%4)*44
		yy := 84 + float64(i/4)*12
		pdf.SetXY(x, yy)
		pdf.SetFont("Helvetica", "", 8)
		grey()
		pdf.CellFormat(42, 4, it[0], "", 0, "L", false, 0, "")
		pdf.SetXY(x, yy+4)
		pdf.SetFont("Helvetica", "B", 10)
		ink()
		pdf.CellFormat(42, 6, it[1], "", 0, "L", false, 0, "")
	}

	// Schedule
	widths := []float64{10, 28, 28, 27, 28, 29, 30}
	heads := []string{"No.", "Due date", "Instalment", "Interest", "Principal", "Balance", "Status"}
	align := []string{"L", "L", "R", "R", "R", "R", "L"}
	header := func() {
		pdf.SetFillColor(27, 35, 64)
		pdf.SetTextColor(255, 255, 255)
		pdf.SetFont("Helvetica", "B", 9)
		for i, h := range heads {
			pdf.CellFormat(widths[i], 8, h, "", 0, align[i], true, 0, "")
		}
		pdf.Ln(8)
	}
	pdf.SetXY(15, 116)
	pdf.SetFont("Helvetica", "B", 12)
	ink()
	pdf.CellFormat(180, 7, "Repayment schedule", "", 1, "L", false, 0, "")
	pdf.Ln(1)
	header()

	pdf.SetFont("Helvetica", "", 9)
	for i, r := range inv.Rows {
		if pdf.GetY()+7 > 280 {
			pdf.AddPage()
			pdf.SetY(15)
			header()
			pdf.SetFont("Helvetica", "", 9)
		}
		pdf.SetFillColor(247, 245, 240)
		fill := i%2 == 1
		ink()
		if r.Status == StatusOverdue {
			pdf.SetTextColor(179, 64, 47)
		}
		cells := []string{strconv.Itoa(r.N), day(r.Due), money(r.PaymentKES), money(r.InterestKES),
			money(r.PrincipalKES), money(r.BalanceKES), r.Status}
		for j, c := range cells {
			pdf.CellFormat(widths[j], 7, c, "", 0, align[j], fill, 0, "")
		}
		pdf.Ln(7)
	}

	// Totals
	room(10)
	var interest, principal float64
	for _, r := range inv.Rows {
		interest += r.InterestKES
		principal += r.PrincipalKES
	}
	pdf.SetDrawColor(27, 35, 64)
	pdf.Line(15, pdf.GetY(), 195, pdf.GetY())
	pdf.SetFont("Helvetica", "B", 9)
	ink()
	pdf.CellFormat(widths[0]+widths[1], 8, "Totals", "", 0, "L", false, 0, "")
	pdf.CellFormat(widths[2], 8, money(inv.TotalRepayableKES), "", 0, "R", false, 0, "")
	pdf.CellFormat(widths[3], 8, money(interest), "", 0, "R", false, 0, "")
	pdf.CellFormat(widths[4], 8, money(principal), "", 1, "R", false, 0, "")

	// Payments received
	if len(inv.Payments) > 0 {
		room(26)
		pdf.Ln(4)
		pdf.SetFont("Helvetica", "B", 12)
		pdf.CellFormat(180, 7, "Payments received", "", 1, "L", false, 0, "")
		pdf.Ln(1)
		pw := []float64{34, 40, 66, 40}
		ph := []string{"Date", "Method", "Reference", "Amount"}
		pa := []string{"L", "L", "L", "R"}
		pdf.SetFillColor(27, 35, 64)
		pdf.SetTextColor(255, 255, 255)
		pdf.SetFont("Helvetica", "B", 9)
		for i, h := range ph {
			pdf.CellFormat(pw[i], 8, h, "", 0, pa[i], true, 0, "")
		}
		pdf.Ln(8)
		pdf.SetFont("Helvetica", "", 9)
		for i, p := range inv.Payments {
			room(7)
			pdf.SetFillColor(247, 245, 240)
			ink()
			for j, c := range []string{day(p.PaidAt), p.MethodLabel, tr(p.Reference), money(p.AmountKES)} {
				pdf.CellFormat(pw[j], 7, c, "", 0, pa[j], i%2 == 1, 0, "")
			}
			pdf.Ln(7)
		}
	}

	// Note
	room(24)
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "", 8)
	grey()
	pdf.SetX(15)
	pdf.MultiCell(180, 4.2, tr(inv.note()), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
