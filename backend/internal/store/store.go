package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"loanscore/data"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
)

type Store struct{ DB *pgxpool.Pool }

type User struct {
	ID           int64
	Email        string
	FullName     string
	PasswordHash string
}

type Profile struct {
	Age                int
	AnnualIncomeKES    float64
	HomeOwnership      string // RENT, OWN, MORTGAGE, OTHER
	EmploymentYears    float64
	LoanIntent         string // PERSONAL, EDUCATION, MEDICAL, VENTURE, HOMEIMPROVEMENT, DEBTCONSOLIDATION
	RequestedAmountKES float64
	DefaultOnFile      string // Y or N
	CreditHistoryYears int
}

type Assessment struct {
	ID          int64
	Approved    bool
	AmountKES   float64
	Probability float64
	CreatedAt   time.Time
}

func New(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, data.Schema); err != nil {
		return nil, err
	}
	return &Store{DB: pool}, nil
}

// NewToken returns a random token (sent to the client) and its SHA-256 hash (stored).
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)
	return token, HashToken(token)
}

func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Store) CreateUser(ctx context.Context, email, name, hash string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx,
		`INSERT INTO users(email, full_name, password_hash) VALUES($1,$2,$3) RETURNING id`,
		email, name, hash).Scan(&id)
	return id, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(ctx,
		`SELECT id, email, full_name, password_hash FROM users WHERE email=$1`, email).
		Scan(&u.ID, &u.Email, &u.FullName, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	tok, hash := NewToken()
	_, err := s.DB.Exec(ctx,
		`INSERT INTO sessions(token_hash, user_id, expires_at) VALUES($1,$2,$3)`,
		hash, userID, time.Now().Add(ttl))
	return tok, err
}

func (s *Store) UserBySession(ctx context.Context, token string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(ctx, `
		SELECT u.id, u.email, u.full_name, u.password_hash FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, HashToken(token)).
		Scan(&u.ID, &u.Email, &u.FullName, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, HashToken(token))
	return err
}

func (s *Store) CreateResetToken(ctx context.Context, userID int64) (string, error) {
	tok, hash := NewToken()
	_, err := s.DB.Exec(ctx,
		`INSERT INTO password_resets(token_hash, user_id, expires_at) VALUES($1,$2,$3)`,
		hash, userID, time.Now().Add(time.Hour))
	return tok, err
}

// ConsumeResetToken validates a one-time token, sets the new password and signs out all sessions.
func (s *Store) ConsumeResetToken(ctx context.Context, token, newHash string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid int64
	err = tx.QueryRow(ctx, `
		UPDATE password_resets SET used=true
		WHERE token_hash=$1 AND used=false AND expires_at > now()
		RETURNING user_id`, HashToken(token)).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, newHash, uid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpsertProfile(ctx context.Context, uid int64, p Profile) error {
	_, err := s.DB.Exec(ctx, `
		INSERT INTO applicant_profiles(user_id, age, annual_income_kes, home_ownership, employment_years,
			loan_intent, requested_amount_kes, default_on_file, credit_history_years)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (user_id) DO UPDATE SET
			age=$2, annual_income_kes=$3, home_ownership=$4, employment_years=$5, loan_intent=$6,
			requested_amount_kes=$7, default_on_file=$8, credit_history_years=$9, updated_at=now()`,
		uid, p.Age, p.AnnualIncomeKES, p.HomeOwnership, p.EmploymentYears, p.LoanIntent,
		p.RequestedAmountKES, p.DefaultOnFile, p.CreditHistoryYears)
	return err
}

func (s *Store) GetProfile(ctx context.Context, uid int64) (*Profile, error) {
	p := &Profile{}
	err := s.DB.QueryRow(ctx, `
		SELECT age, annual_income_kes, home_ownership, employment_years, loan_intent,
		       requested_amount_kes, default_on_file, credit_history_years
		FROM applicant_profiles WHERE user_id=$1`, uid).
		Scan(&p.Age, &p.AnnualIncomeKES, &p.HomeOwnership, &p.EmploymentYears, &p.LoanIntent,
			&p.RequestedAmountKES, &p.DefaultOnFile, &p.CreditHistoryYears)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) SaveAssessment(ctx context.Context, uid int64, a Assessment) error {
	_, err := s.DB.Exec(ctx,
		`INSERT INTO assessments(user_id, approved, amount_kes, probability) VALUES($1,$2,$3,$4)`,
		uid, a.Approved, a.AmountKES, a.Probability)
	return err
}

func (s *Store) LatestAssessment(ctx context.Context, uid int64) (*Assessment, error) {
	a := &Assessment{}
	err := s.DB.QueryRow(ctx, `
		SELECT id, approved, amount_kes, probability, created_at FROM assessments
		WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, uid).
		Scan(&a.ID, &a.Approved, &a.AmountKES, &a.Probability, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// AssessmentByID returns an assessment only if it belongs to the given user.
func (s *Store) AssessmentByID(ctx context.Context, uid, id int64) (*Assessment, error) {
	a := &Assessment{}
	err := s.DB.QueryRow(ctx, `
		SELECT id, approved, amount_kes, probability, created_at FROM assessments
		WHERE id=$1 AND user_id=$2`, id, uid).
		Scan(&a.ID, &a.Approved, &a.AmountKES, &a.Probability, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

type LoanApplication struct {
	ID                    int64
	AssessmentID          int64
	AmountKES             float64
	Status                string // pending, approved, rejected, disbursed, cancelled
	CreatedAt             time.Time
	AnnualRatePct         float64 // 0 on applications made before pricing existed
	TermMonths            int
	MonthlyInstallmentKES float64
	TotalRepayableKES     float64
	DisbursedAt           *time.Time // set when the loan is paid out
}

// LoanTerms are the repayment terms that were shown to the customer when they applied.
type LoanTerms struct {
	AnnualRatePct         float64
	Months                int
	MonthlyInstallmentKES float64
	TotalRepayableKES     float64
}

// CreateLoanApplication records an application as "pending". It returns ErrDuplicate if this
// assessment was already used, or the customer already has an application waiting to be processed.
func (s *Store) CreateLoanApplication(ctx context.Context, uid, assessmentID int64, amount float64, t LoanTerms) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `
		INSERT INTO loan_applications(user_id, assessment_id, amount_kes, annual_rate_pct, term_months,
			monthly_installment_kes, total_repayable_kes)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		uid, assessmentID, amount, t.AnnualRatePct, t.Months, t.MonthlyInstallmentKES, t.TotalRepayableKES).Scan(&id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return 0, ErrDuplicate
	}
	return id, err
}

func (s *Store) LatestLoanApplication(ctx context.Context, uid int64) (*LoanApplication, error) {
	l := &LoanApplication{}
	err := s.DB.QueryRow(ctx, `
		SELECT id, assessment_id, amount_kes, status, created_at, annual_rate_pct, term_months,
		       monthly_installment_kes, total_repayable_kes, disbursed_at
		FROM loan_applications WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, uid).
		Scan(&l.ID, &l.AssessmentID, &l.AmountKES, &l.Status, &l.CreatedAt, &l.AnnualRatePct, &l.TermMonths,
			&l.MonthlyInstallmentKES, &l.TotalRepayableKES, &l.DisbursedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// LoanApplicationByID returns one of this customer's applications. Other customers' applications are never returned.
func (s *Store) LoanApplicationByID(ctx context.Context, uid, id int64) (*LoanApplication, error) {
	l := &LoanApplication{}
	err := s.DB.QueryRow(ctx, `
		SELECT id, assessment_id, amount_kes, status, created_at, annual_rate_pct, term_months,
		       monthly_installment_kes, total_repayable_kes, disbursed_at
		FROM loan_applications WHERE id=$1 AND user_id=$2`, id, uid).
		Scan(&l.ID, &l.AssessmentID, &l.AmountKES, &l.Status, &l.CreatedAt, &l.AnnualRatePct, &l.TermMonths,
			&l.MonthlyInstallmentKES, &l.TotalRepayableKES, &l.DisbursedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

var (
	ErrNotDisbursed  = errors.New("loan has not been paid out")
	ErrBadTransition = errors.New("loan is not in the right state for that change")
)

// OverpayError means a payment is larger than what is still owed.
type OverpayError struct{ OutstandingKES float64 }

func (e *OverpayError) Error() string {
	return fmt.Sprintf("payment is more than the outstanding balance of KES %.0f", e.OutstandingKES)
}

type Payment struct {
	ID        int64
	AmountKES float64
	Method    string
	Reference string
	PaidAt    time.Time
}

// ListPayments returns the payments recorded against a loan, oldest first.
func (s *Store) ListPayments(ctx context.Context, loanID int64) ([]Payment, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, amount_kes, method, reference, paid_at FROM loan_payments
		WHERE loan_id=$1 ORDER BY paid_at, id`, loanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.AmountKES, &p.Method, &p.Reference, &p.PaidAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPayment saves a payment against a paid-out loan. maxTotalKES is the total the customer owes
// (the whole schedule); a payment that would take the total paid above it is refused. A reference
// that was already recorded returns ErrDuplicate.
func (s *Store) RecordPayment(ctx context.Context, loanID int64, amount float64, method, reference string,
	paidAt time.Time, by int64, maxTotalKES float64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM loan_applications WHERE id=$1 FOR UPDATE`, loanID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "disbursed" {
		return ErrNotDisbursed
	}
	var paid float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kes),0)::float8 FROM loan_payments WHERE loan_id=$1`, loanID).Scan(&paid); err != nil {
		return err
	}
	if paid+amount > maxTotalKES+0.005 {
		return &OverpayError{OutstandingKES: maxTotalKES - paid}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO loan_payments(loan_id, amount_kes, method, reference, paid_at, recorded_by)
		VALUES($1,$2,$3,$4,$5,$6)`, loanID, amount, method, reference, paidAt, by)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetLoanStatus moves a loan from one status to another (for example pending to approved).
// It returns ErrBadTransition if the loan is not currently in the "from" status.
func (s *Store) SetLoanStatus(ctx context.Context, id int64, from, to string) error {
	tag, err := s.DB.Exec(ctx, `
		UPDATE loan_applications
		SET status=$3::text, disbursed_at = CASE WHEN $3::text = 'disbursed' THEN now() ELSE disbursed_at END
		WHERE id=$1 AND status=$2::text`, id, from, to)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBadTransition
	}
	return nil
}

// AdminLoan is a loan application together with its customer and how much has been paid.
type AdminLoan struct {
	LoanApplication
	UserID   int64
	FullName string
	Email    string
	PaidKES  float64
}

const adminLoanSelect = `
	SELECT l.id, l.assessment_id, l.amount_kes, l.status, l.created_at, l.annual_rate_pct, l.term_months,
	       l.monthly_installment_kes, l.total_repayable_kes, l.disbursed_at,
	       u.id, u.full_name, u.email,
	       COALESCE((SELECT SUM(p.amount_kes) FROM loan_payments p WHERE p.loan_id = l.id), 0)::float8
	FROM loan_applications l JOIN users u ON u.id = l.user_id`

func scanAdminLoan(row pgx.Row, l *AdminLoan) error {
	return row.Scan(&l.ID, &l.AssessmentID, &l.AmountKES, &l.Status, &l.CreatedAt, &l.AnnualRatePct, &l.TermMonths,
		&l.MonthlyInstallmentKES, &l.TotalRepayableKES, &l.DisbursedAt, &l.UserID, &l.FullName, &l.Email, &l.PaidKES)
}

// AdminListLoans returns the most recent applications from all customers, newest first.
func (s *Store) AdminListLoans(ctx context.Context) ([]AdminLoan, error) {
	rows, err := s.DB.Query(ctx, adminLoanSelect+` ORDER BY l.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminLoan
	for rows.Next() {
		var l AdminLoan
		if err := scanAdminLoan(rows, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AdminLoanByID returns any customer's application. Only call it for signed-in staff.
func (s *Store) AdminLoanByID(ctx context.Context, id int64) (*AdminLoan, error) {
	l := &AdminLoan{}
	err := scanAdminLoan(s.DB.QueryRow(ctx, adminLoanSelect+` WHERE l.id=$1`, id), l)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// ListLoanApplications returns all of a customer's applications, newest first.
func (s *Store) ListLoanApplications(ctx context.Context, uid int64) ([]LoanApplication, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, assessment_id, amount_kes, status, created_at, annual_rate_pct, term_months,
		       monthly_installment_kes, total_repayable_kes, disbursed_at
		FROM loan_applications WHERE user_id=$1 ORDER BY id DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoanApplication
	for rows.Next() {
		var l LoanApplication
		if err := rows.Scan(&l.ID, &l.AssessmentID, &l.AmountKES, &l.Status, &l.CreatedAt, &l.AnnualRatePct, &l.TermMonths,
			&l.MonthlyInstallmentKES, &l.TotalRepayableKES, &l.DisbursedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
