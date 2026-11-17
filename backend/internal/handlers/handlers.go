package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"loanscore/backend/internal/mailer"
	"loanscore/backend/internal/ml"
	"loanscore/backend/internal/pricing"
	"loanscore/backend/internal/statement"
	"loanscore/backend/internal/store"
	"loanscore/frontend"
)

const (
	cookieName = "session"
	sessionTTL = 7 * 24 * time.Hour
)

var intentLabels = map[string]string{
	"PERSONAL": "Personal", "EDUCATION": "Education", "MEDICAL": "Medical",
	"VENTURE": "Business", "HOMEIMPROVEMENT": "Home improvement", "DEBTCONSOLIDATION": "Debt consolidation",
}

var homeOwnership = map[string]bool{"RENT": true, "OWN": true, "MORTGAGE": true, "OTHER": true}

type Deps struct {
	Store        *store.Store
	Mail         *mailer.Mailer
	ML           *ml.Client
	Pricing      pricing.Config // zero value means: read PRICING_* environment variables
	BaseURL      string
	SecureCookie bool
	Issuer       string // name printed on invoices; defaults to COMPANY_NAME, then "Kopa Check"
	// AdminEmails are the staff who can approve loans and record payments; defaults to ADMIN_EMAILS (comma-separated).
	AdminEmails []string
	// PaymentInstructions is shown to customers with an active loan, e.g. "Pay by M-Pesa Paybill 123456, account LN-000001".
	PaymentInstructions string
}

type Server struct {
	Deps
	tpl    map[string]*template.Template
	admins map[string]bool
}

func kes(v float64) string {
	s := strconv.FormatInt(int64(v+0.5), 10)
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return "KES " + string(out)
}

func New(d Deps) *Server {
	if d.Pricing == (pricing.Config{}) {
		d.Pricing = pricing.FromEnv()
	}
	if d.Issuer == "" {
		d.Issuer = os.Getenv("COMPANY_NAME")
	}
	if d.Issuer == "" {
		d.Issuer = "Kopa Check"
	}
	if len(d.AdminEmails) == 0 {
		d.AdminEmails = strings.Split(os.Getenv("ADMIN_EMAILS"), ",")
	}
	if d.PaymentInstructions == "" {
		d.PaymentInstructions = os.Getenv("PAYMENT_INSTRUCTIONS")
	}
	s := &Server{Deps: d, tpl: map[string]*template.Template{}, admins: map[string]bool{}}
	for _, e := range d.AdminEmails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			s.admins[e] = true
		}
	}
	funcs := template.FuncMap{
		"kes":     kes,
		"percent": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"date":    func(t time.Time) string { return t.Format("2 Jan 2006, 15:04") },
		"monthly": func(annual float64) float64 { return annual / 12 },
		"day":     func(t time.Time) string { return t.Format("2 Jan 2006") },
		"isStaff": func(u *store.User) bool { return s.isStaff(u) },
		"ref":     func(id int64) string { return fmt.Sprintf("LN-%06d", id) },
		"months": func(n int) string {
			if n == 1 {
				return "1 month"
			}
			return fmt.Sprintf("%d months", n)
		},
		"intent": func(k string) string {
			if v, ok := intentLabels[k]; ok {
				return v
			}
			return k
		},
	}
	for _, page := range []string{"login", "register", "forgot", "reset", "dashboard", "profile", "invoice", "admin_loans", "admin_loan"} {
		s.tpl[page] = template.Must(template.New("base").Funcs(funcs).
			ParseFS(frontend.FS, "templates/base.html", "templates/"+page+".html"))
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /forgot", s.forgotForm)
	mux.HandleFunc("POST /forgot", s.forgot)
	mux.HandleFunc("GET /reset", s.resetForm)
	mux.HandleFunc("POST /reset", s.reset)
	mux.HandleFunc("GET /dashboard", s.auth(s.dashboard))
	mux.HandleFunc("GET /profile", s.auth(s.profileForm))
	mux.HandleFunc("POST /profile", s.auth(s.profileSave))
	mux.HandleFunc("POST /assess", s.auth(s.assess))
	mux.HandleFunc("POST /apply", s.auth(s.apply))
	mux.HandleFunc("GET /terms", s.auth(s.terms))
	mux.HandleFunc("GET /invoice", s.auth(s.invoicePage))
	mux.HandleFunc("GET /invoice/{id}", s.auth(s.invoicePage))
	mux.HandleFunc("GET /statement/{id}", s.auth(s.statementPDF))
	mux.HandleFunc("GET /admin/loans", s.staff(s.adminLoans))
	mux.HandleFunc("GET /admin/loans/{id}", s.staff(s.adminLoan))
	mux.HandleFunc("POST /admin/loans/{id}/status", s.staff(s.adminStatus))
	mux.HandleFunc("POST /admin/loans/{id}/payments", s.staff(s.adminPayment))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func (s *Server) render(w http.ResponseWriter, page string, data map[string]any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tpl[page].ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

func (s *Server) currentUser(r *http.Request) *store.User {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	u, err := s.Store.UserBySession(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return u
}

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, u)
	}
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, uid int64) error {
	tok, err := s.Store.CreateSession(ctx, uid, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		Secure: s.SecureCookie, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
	return nil
}

func validPassword(p string) bool { return len(p) >= 8 && len(p) <= 72 }

// ---------- auth pages ----------

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "register", map[string]any{}, 200)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("full_name"))
	addr, err := mail.ParseAddress(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	fail := func(msg string) {
		s.render(w, "register", map[string]any{"Error": msg, "Name": name}, http.StatusUnprocessableEntity)
	}
	switch {
	case name == "":
		fail("Enter your full name.")
		return
	case err != nil:
		fail("Enter a valid email address.")
		return
	case !validPassword(pw):
		fail("Use a password of 8 to 72 characters.")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	email := strings.ToLower(addr.Address)
	id, err := s.Store.CreateUser(r.Context(), email, name, string(hash))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			fail("An account with this email already exists. Log in instead.")
			return
		}
		log.Printf("create user: %v", err)
		http.Error(w, "Something went wrong", 500)
		return
	}
	if err := s.startSession(r.Context(), w, id); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login", map[string]any{"Info": r.URL.Query().Get("info")}, 200)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	u, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(r.FormValue("password"))) != nil {
		s.render(w, "login", map[string]any{"Error": "Email or password is incorrect.", "Email": email}, http.StatusUnauthorized)
		return
	}
	if err := s.startSession(r.Context(), w, u.ID); err != nil {
		http.Error(w, "Something went wrong", 500)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login?info=You+have+been+logged+out.", http.StatusSeeOther)
}

// ---------- password reset ----------

func (s *Server) forgotForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "forgot", map[string]any{}, 200)
}

func (s *Server) forgot(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if u, err := s.Store.UserByEmail(r.Context(), email); err == nil {
		if tok, err := s.Store.CreateResetToken(r.Context(), u.ID); err == nil {
			link := s.BaseURL + "/reset?token=" + tok
			body := "Hello " + u.FullName + ",\n\nUse this link to set a new password. It expires in 1 hour:\n\n" +
				link + "\n\nIf you did not ask for this, ignore this email."
			go func() {
				if err := s.Mail.Send(u.Email, "Reset your password", body); err != nil {
					log.Printf("send reset email: %v", err)
				}
			}()
		}
	}
	// Same response whether or not the account exists, so emails can't be probed.
	s.render(w, "forgot", map[string]any{"Sent": true}, 200)
}

func (s *Server) resetForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "reset", map[string]any{"Token": r.URL.Query().Get("token")}, 200)
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	tok, pw := r.FormValue("token"), r.FormValue("password")
	if !validPassword(pw) {
		s.render(w, "reset", map[string]any{"Token": tok, "Error": "Use a password of 8 to 72 characters."}, 422)
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err := s.Store.ConsumeResetToken(r.Context(), tok, string(hash)); err != nil {
		s.render(w, "reset", map[string]any{"Token": tok, "Error": "This reset link is invalid or has expired. Request a new one."}, 400)
		return
	}
	http.Redirect(w, r, "/login?info=Password+updated.+Log+in+with+your+new+password.", http.StatusSeeOther)
}

// ---------- profile + assessment ----------

// dashboardData gathers everything the dashboard page shows for one customer.
func (s *Server) dashboardData(ctx context.Context, u *store.User) map[string]any {
	data := map[string]any{"User": u}
	var prof *store.Profile
	var latest *store.Assessment
	data["PaymentInstructions"] = s.PaymentInstructions
	if p, err := s.Store.GetProfile(ctx, u.ID); err == nil {
		prof = p
		data["Profile"] = p
	}
	if a, err := s.Store.LatestAssessment(ctx, u.ID); err == nil {
		data["Assessment"] = a
		latest = a
		if a.Approved && prof != nil { // show the repayment terms for the full qualifying amount
			if t, err := s.Pricing.Quote(a.AmountKES, a.Probability, prof.AnnualIncomeKES/12); err != nil {
				data["TermsError"] = priceMessage(err)
			} else {
				data["Terms"] = &t
			}
		}
	}
	if l, err := s.Store.LatestLoanApplication(ctx, u.ID); err == nil {
		data["Application"] = l
		if inv := s.invoiceFor(ctx, u.FullName, u.Email, l); inv != nil {
			data["Invoice"] = inv
			data["LoanCard"] = showLoanCard(l, inv, latest)
		}
	}
	return data
}

// showLoanCard says whether the dashboard's main card should describe the loan (approved, active or just repaid)
// instead of the eligibility check. Once a loan is repaid, the card gives way to the eligibility check as soon as the
// customer runs an assessment after the final payment, so they can borrow again.
func showLoanCard(l *store.LoanApplication, inv *statement.Invoice, latest *store.Assessment) bool {
	switch l.Status {
	case "approved":
		return true
	case "disbursed":
		if inv.Repaid && latest != nil && len(inv.Payments) > 0 {
			lastPaid := inv.Payments[0].PaidAt
			for _, p := range inv.Payments {
				if p.PaidAt.After(lastPaid) {
					lastPaid = p.PaidAt
				}
			}
			if latest.CreatedAt.After(lastPaid) {
				return false
			}
		}
		return true
	}
	return false
}

func (s *Server) inputFor(name, email string, l *store.LoanApplication, pays []store.Payment) statement.Input {
	in := statement.Input{
		ID: l.ID, Status: l.Status, CreatedAt: l.CreatedAt, DisbursedAt: l.DisbursedAt, AmountKES: l.AmountKES,
		AnnualRatePct: l.AnnualRatePct, Months: l.TermMonths, InstallmentKES: l.MonthlyInstallmentKES,
		CustomerName: name, CustomerEmail: email, Issuer: s.Issuer,
	}
	for _, p := range pays {
		in.Payments = append(in.Payments, statement.PaymentIn{ID: p.ID, AmountKES: p.AmountKES, PaidAt: p.PaidAt, Method: p.Method, Reference: p.Reference})
	}
	return in
}

// invoiceFor builds the loan invoice for an application, or nil if there is nothing to invoice
// (no repayment terms saved, or the application was rejected or cancelled).
func (s *Server) invoiceFor(ctx context.Context, name, email string, l *store.LoanApplication) *statement.Invoice {
	if l.Status == "rejected" || l.Status == "cancelled" {
		return nil
	}
	pays, err := s.Store.ListPayments(ctx, l.ID)
	if err != nil {
		log.Printf("list payments: %v", err)
	}
	return statement.Build(s.inputFor(name, email, l, pays))
}

// invoiceable reports whether an application has an invoice to show.
func invoiceable(l *store.LoanApplication) bool {
	return l.TermMonths > 0 && l.Status != "rejected" && l.Status != "cancelled"
}

// invoicePage shows one loan invoice in the browser: the one asked for in the address, or the newest one.
func (s *Server) invoicePage(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx := r.Context()
	loans, err := s.Store.ListLoanApplications(ctx, u.ID)
	if err != nil {
		log.Printf("list loans: %v", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	var list []store.LoanApplication
	for _, l := range loans {
		if invoiceable(&l) {
			list = append(list, l)
		}
	}
	var chosen *store.LoanApplication
	if v := r.PathValue("id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		for i := range list { // only this customer's own invoices are in the list
			if list[i].ID == id {
				chosen = &list[i]
			}
		}
		if chosen == nil {
			http.NotFound(w, r)
			return
		}
	} else if len(list) > 0 {
		chosen = &list[0]
	}
	data := map[string]any{"User": u, "Loans": list}
	if chosen != nil {
		if inv := s.invoiceFor(ctx, u.FullName, u.Email, chosen); inv != nil {
			data["Invoice"] = inv
			data["CurrentID"] = chosen.ID
		}
	}
	s.render(w, "invoice", data, 200)
}

// statementPDF sends the invoice for one of the signed-in customer's applications as a PDF download.
func (s *Server) statementPDF(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.Store.LoanApplicationByID(r.Context(), u.ID, id) // someone else's application looks like "not found"
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv := s.invoiceFor(r.Context(), u.FullName, u.Email, l)
	if inv == nil {
		http.NotFound(w, r)
		return
	}
	pdf, err := inv.PDF()
	if err != nil {
		log.Printf("invoice pdf: %v", err)
		http.Error(w, "Could not create the invoice. Please try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+inv.Number+`.pdf"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(pdf)
}

// ---------- staff: approve loans, record payments ----------

func (s *Server) isStaff(u *store.User) bool { return u != nil && s.admins[strings.ToLower(u.Email)] }

// staff wraps a handler so only signed-in staff can reach it. Everyone else gets "not found".
func (s *Server) staff(next func(http.ResponseWriter, *http.Request, *store.User)) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request, u *store.User) {
		if !s.isStaff(u) {
			http.NotFound(w, r)
			return
		}
		next(w, r, u)
	})
}

func (s *Server) adminLoans(w http.ResponseWriter, r *http.Request, u *store.User) {
	loans, err := s.Store.AdminListLoans(r.Context())
	if err != nil {
		log.Printf("admin list: %v", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	type row struct {
		store.AdminLoan
		OutstandingKES float64
		Active         bool
	}
	rows := make([]row, 0, len(loans))
	for _, l := range loans {
		rw := row{AdminLoan: l}
		if l.Status == "disbursed" {
			if inv := statement.Build(s.inputFor(l.FullName, l.Email, &l.LoanApplication, nil)); inv != nil {
				rw.Active = true
				rw.OutstandingKES = inv.TotalRepayableKES - l.PaidKES
				if rw.OutstandingKES < 0.5 {
					rw.OutstandingKES = 0
				}
			}
		}
		rows = append(rows, rw)
	}
	s.render(w, "admin_loans", map[string]any{"User": u, "Loans": rows}, 200)
}

func (s *Server) adminLoan(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.Store.AdminLoanByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pays, _ := s.Store.ListPayments(r.Context(), id)
	type method struct{ Key, Label string }
	var methods []method
	for _, k := range statement.MethodOrder {
		methods = append(methods, method{k, statement.MethodLabels[k]})
	}
	s.render(w, "admin_loan", map[string]any{
		"User": u, "Loan": l, "Methods": methods,
		"Invoice": statement.Build(s.inputFor(l.FullName, l.Email, &l.LoanApplication, pays)),
		"Info":    r.URL.Query().Get("info"), "Error": r.URL.Query().Get("error"),
	}, 200)
}

func adminBack(w http.ResponseWriter, r *http.Request, id int64, kind, msg string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/loans/%d?%s=%s", id, kind, url.QueryEscape(msg)), http.StatusSeeOther)
}

var loanActions = map[string]struct{ From, To, Done string }{
	"approve":  {"pending", "approved", "Loan approved."},
	"reject":   {"pending", "rejected", "Loan rejected."},
	"disburse": {"approved", "disbursed", "Marked as paid out. Repayment dates now run from today."},
	"cancel":   {"approved", "cancelled", "Loan cancelled."},
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	act, ok := loanActions[r.FormValue("action")]
	if !ok {
		adminBack(w, r, id, "error", "Unknown action.")
		return
	}
	if err := s.Store.SetLoanStatus(r.Context(), id, act.From, act.To); err != nil {
		if errors.Is(err, store.ErrBadTransition) {
			adminBack(w, r, id, "error", "This loan is no longer in the right state for that change.")
			return
		}
		log.Printf("set status: %v", err)
		adminBack(w, r, id, "error", "Something went wrong. Please try again.")
		return
	}
	log.Printf("staff %s: loan %d %s -> %s", u.Email, id, act.From, act.To)
	adminBack(w, r, id, "info", act.Done)
}

func (s *Server) adminPayment(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.Store.AdminLoanByID(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv := statement.Build(s.inputFor(l.FullName, l.Email, &l.LoanApplication, nil))
	if inv == nil {
		adminBack(w, r, id, "error", "This loan has no repayment terms.")
		return
	}
	amount, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	method := r.FormValue("method")
	ref := strings.TrimSpace(r.FormValue("reference"))
	_, validMethod := statement.MethodLabels[method]
	now := time.Now()
	paidAt := now
	var msg string
	switch {
	case amount <= 0 || amount > 1e9:
		msg = "Enter the amount received in KES."
	case !validMethod:
		msg = "Choose how the payment was made."
	case len(ref) > 40:
		msg = "The reference is too long."
	case (method == "mpesa" || method == "bank") && ref == "":
		msg = "Enter the M-Pesa or bank reference."
	}
	if v := strings.TrimSpace(r.FormValue("paid_on")); msg == "" && v != "" {
		d, perr := time.ParseInLocation("2006-01-02", v, statement.Location())
		nowEAT := now.In(statement.Location())
		todayStart := time.Date(nowEAT.Year(), nowEAT.Month(), nowEAT.Day(), 0, 0, 0, 0, statement.Location())
		switch {
		case perr != nil:
			msg = "Enter the payment date as a valid date."
		case d.After(todayStart):
			msg = "The payment date cannot be in the future."
		case d.Equal(todayStart):
			paidAt = now
		default:
			paidAt = d.Add(12 * time.Hour)
		}
	}
	if msg != "" {
		adminBack(w, r, id, "error", msg)
		return
	}
	err = s.Store.RecordPayment(ctx, id, amount, method, ref, paidAt, u.ID, inv.TotalRepayableKES)
	var over *store.OverpayError
	switch {
	case err == nil:
		log.Printf("staff %s: recorded KES %.0f on loan %d (%s %s)", u.Email, amount, id, method, ref)
		adminBack(w, r, id, "info", "Payment recorded.")
	case errors.Is(err, store.ErrNotDisbursed):
		adminBack(w, r, id, "error", "Payments can only be recorded after the loan has been paid out.")
	case errors.Is(err, store.ErrDuplicate):
		adminBack(w, r, id, "error", "A payment with that reference is already recorded.")
	case errors.As(err, &over):
		adminBack(w, r, id, "error", fmt.Sprintf("That is more than the outstanding balance of %s.", kes(over.OutstandingKES)))
	default:
		log.Printf("record payment: %v", err)
		adminBack(w, r, id, "error", "Something went wrong. Please try again.")
	}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, u *store.User) {
	data := s.dashboardData(r.Context(), u)
	data["Info"] = r.URL.Query().Get("info")
	s.render(w, "dashboard", data, 200)
}

func (s *Server) profileForm(w http.ResponseWriter, r *http.Request, u *store.User) {
	p, err := s.Store.GetProfile(r.Context(), u.ID)
	if err != nil {
		p = &store.Profile{}
	}
	s.render(w, "profile", map[string]any{"User": u, "Profile": p}, 200)
}

func (s *Server) profileSave(w http.ResponseWriter, r *http.Request, u *store.User) {
	num := func(k string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(r.FormValue(k)), 64); return v }
	whole := func(k string) int { v, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k))); return v }
	p := store.Profile{
		Age:                whole("age"),
		AnnualIncomeKES:    num("monthly_income") * 12,
		HomeOwnership:      r.FormValue("home_ownership"),
		EmploymentYears:    num("employment_years"),
		LoanIntent:         r.FormValue("loan_intent"),
		RequestedAmountKES: num("requested_amount"),
		DefaultOnFile:      r.FormValue("default_on_file"),
		CreditHistoryYears: whole("credit_history_years"),
	}
	var msg string
	switch {
	case p.Age < 18 || p.Age > 100:
		msg = "You must be between 18 and 100 to apply."
	case p.AnnualIncomeKES <= 0:
		msg = "Enter your monthly income in KES."
	case !homeOwnership[p.HomeOwnership]:
		msg = "Select your housing situation."
	case p.EmploymentYears < 0 || p.EmploymentYears > 60:
		msg = "Enter years in current work between 0 and 60."
	case intentLabels[p.LoanIntent] == "":
		msg = "Select what the loan is for."
	case p.RequestedAmountKES <= 0:
		msg = "Enter the loan amount you want in KES."
	case p.DefaultOnFile != "Y" && p.DefaultOnFile != "N":
		msg = "Say whether you have ever defaulted on a loan."
	case p.CreditHistoryYears < 0 || p.CreditHistoryYears > 60:
		msg = "Enter your credit history length between 0 and 60 years."
	}
	if msg != "" {
		s.render(w, "profile", map[string]any{"User": u, "Profile": &p, "Error": msg}, http.StatusUnprocessableEntity)
		return
	}
	if err := s.Store.UpsertProfile(r.Context(), u.ID, p); err != nil {
		log.Printf("save profile: %v", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard?info=Details+saved.+Run+your+assessment+below.", http.StatusSeeOther)
}

func (s *Server) assess(w http.ResponseWriter, r *http.Request, u *store.User) {
	p, err := s.Store.GetProfile(r.Context(), u.ID)
	if err != nil {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}
	res, err := s.ML.Predict(r.Context(), ml.Request{
		Age: p.Age, AnnualIncomeKES: p.AnnualIncomeKES, HomeOwnership: p.HomeOwnership,
		EmploymentYears: p.EmploymentYears, LoanIntent: p.LoanIntent, RequestedAmountKES: p.RequestedAmountKES,
		DefaultOnFile: p.DefaultOnFile, CreditHistoryYears: p.CreditHistoryYears,
	})
	if err != nil {
		log.Printf("ml predict: %v", err)
		http.Redirect(w, r, "/dashboard?info=The+scoring+service+is+unavailable.+Try+again+in+a+moment.", http.StatusSeeOther)
		return
	}
	_ = s.Store.SaveAssessment(r.Context(), u.ID, store.Assessment{
		Approved: res.Approved, AmountKES: res.AmountKES, Probability: res.Probability,
	})
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// minApplyKES is the smallest loan a customer can apply for (unless their qualifying amount is lower).
const minApplyKES = 1000.0

// apply turns an approved assessment into a loan application. Everything is re-checked on the server:
// the form only suggests an amount, it never decides what the customer is allowed to borrow.
func (s *Server) apply(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx := r.Context()
	fail := func(msg string, status int) {
		data := s.dashboardData(ctx, u)
		data["Error"] = msg
		s.render(w, "dashboard", data, status)
	}

	aid, err := strconv.ParseInt(r.FormValue("assessment_id"), 10, 64)
	if err != nil {
		fail("Run an assessment before you apply.", http.StatusUnprocessableEntity)
		return
	}
	a, err := s.Store.AssessmentByID(ctx, u.ID, aid) // must belong to this customer
	if err != nil || !a.Approved {
		fail("You can only apply after an approved assessment.", http.StatusUnprocessableEntity)
		return
	}
	if latest, err := s.Store.LatestAssessment(ctx, u.ID); err != nil || latest.ID != a.ID {
		fail("Your assessment is out of date. Run it again, then apply.", http.StatusConflict)
		return
	}

	amount, err := checkAmount(a, r.FormValue("amount"))
	if err != nil {
		fail(err.Error(), http.StatusUnprocessableEntity)
		return
	}
	prof, err := s.Store.GetProfile(ctx, u.ID)
	if err != nil {
		fail("Add your details before you apply.", http.StatusUnprocessableEntity)
		return
	}
	// The repayment terms are worked out here, on the server, from the amount being applied for.
	t, err := s.Pricing.Quote(amount, a.Probability, prof.AnnualIncomeKES/12)
	if err != nil {
		fail(priceMessage(err), http.StatusUnprocessableEntity)
		return
	}

	terms := store.LoanTerms{AnnualRatePct: t.AnnualRatePct, Months: t.Months,
		MonthlyInstallmentKES: t.MonthlyInstallmentKES, TotalRepayableKES: t.TotalRepayableKES}
	if _, err := s.Store.CreateLoanApplication(ctx, u.ID, a.ID, amount, terms); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			// Double-click, or an application is already waiting: just show the one on file.
			if l, lerr := s.Store.LatestLoanApplication(ctx, u.ID); lerr == nil && l.Status == "pending" {
				http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
				return
			}
			fail("You have already applied with this assessment. Run a new assessment to apply again.", http.StatusConflict)
			return
		}
		log.Printf("create loan application: %v", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// checkAmount parses and validates the amount a customer wants, against what they qualify for.
func checkAmount(a *store.Assessment, raw string) (float64, error) {
	amount, err := strconv.ParseFloat(strings.NewReplacer(",", "", " ", "").Replace(raw), 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, errors.New("Enter the amount you want to apply for, in KES.")
	}
	amount = math.Round(amount)
	if amount > a.AmountKES {
		return 0, errors.New("You can apply for up to " + kes(a.AmountKES) + ".")
	}
	if min := math.Min(minApplyKES, a.AmountKES); amount < min {
		return 0, errors.New("The smallest loan you can apply for is " + kes(min) + ".")
	}
	return amount, nil
}

// priceMessage turns a pricing error into something a customer can act on.
func priceMessage(err error) string {
	var ue *pricing.UnaffordableError
	if errors.As(err, &ue) && ue.MaxKES > 0 {
		return "Based on your income, the most we can offer is " + kes(ue.MaxKES) + ". Apply for a smaller amount."
	}
	if errors.As(err, &ue) {
		return "We can't offer a loan that fits your income right now."
	}
	log.Printf("pricing: %v", err)
	return "We couldn't work out the repayment terms. Please try again."
}

// terms returns the repayment terms for an amount as JSON, so the form can update as the customer types.
// It uses the same rules as apply, so what they see is what they get.
func (s *Server) terms(w http.ResponseWriter, r *http.Request, u *store.User) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	bad := func(msg string) { reply(http.StatusUnprocessableEntity, map[string]string{"error": msg}) }

	a, err := s.Store.LatestAssessment(r.Context(), u.ID)
	if err != nil || !a.Approved {
		bad("Run an assessment first.")
		return
	}
	amount, err := checkAmount(a, r.URL.Query().Get("amount"))
	if err != nil {
		bad(err.Error())
		return
	}
	prof, err := s.Store.GetProfile(r.Context(), u.ID)
	if err != nil {
		bad("Add your details first.")
		return
	}
	t, err := s.Pricing.Quote(amount, a.Probability, prof.AnnualIncomeKES/12)
	if err != nil {
		bad(priceMessage(err))
		return
	}
	reply(http.StatusOK, map[string]any{
		"amount": amount, "annual_rate_pct": t.AnnualRatePct, "months": t.Months,
		"monthly_installment": t.MonthlyInstallmentKES, "total_repayable": t.TotalRepayableKES,
		"total_interest": t.TotalInterestKES,
	})
}
