CREATE TABLE IF NOT EXISTS users (
  id BIGSERIAL PRIMARY KEY,
  email TEXT UNIQUE NOT NULL,
  full_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS password_resets (
  token_hash TEXT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  used BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS applicant_profiles (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  age INT NOT NULL,
  annual_income_kes NUMERIC(14,2) NOT NULL,
  home_ownership TEXT NOT NULL,
  employment_years NUMERIC(4,1) NOT NULL,
  loan_intent TEXT NOT NULL,
  requested_amount_kes NUMERIC(14,2) NOT NULL,
  default_on_file TEXT NOT NULL,
  credit_history_years INT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS assessments (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  approved BOOLEAN NOT NULL,
  amount_kes NUMERIC(14,2) NOT NULL,
  probability DOUBLE PRECISION NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS loan_applications (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  assessment_id BIGINT NOT NULL UNIQUE REFERENCES assessments(id) ON DELETE CASCADE,
  amount_kes NUMERIC(14,2) NOT NULL CHECK (amount_kes > 0),
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'approved', 'rejected', 'disbursed', 'cancelled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  annual_rate_pct NUMERIC(5,2) NOT NULL DEFAULT 0,
  term_months INT NOT NULL DEFAULT 0,
  monthly_installment_kes NUMERIC(14,2) NOT NULL DEFAULT 0,
  total_repayable_kes NUMERIC(14,2) NOT NULL DEFAULT 0
);
-- For databases that already have the table from an earlier version (safe to run every start).
ALTER TABLE loan_applications ADD COLUMN IF NOT EXISTS annual_rate_pct NUMERIC(5,2) NOT NULL DEFAULT 0;
ALTER TABLE loan_applications ADD COLUMN IF NOT EXISTS term_months INT NOT NULL DEFAULT 0;
ALTER TABLE loan_applications ADD COLUMN IF NOT EXISTS monthly_installment_kes NUMERIC(14,2) NOT NULL DEFAULT 0;
ALTER TABLE loan_applications ADD COLUMN IF NOT EXISTS total_repayable_kes NUMERIC(14,2) NOT NULL DEFAULT 0;
-- A customer can have only one application waiting to be processed at a time.
CREATE UNIQUE INDEX IF NOT EXISTS one_pending_application_per_user
  ON loan_applications(user_id) WHERE status = 'pending';

-- Payment tracking: when the loan was paid out, and the payments received against it.
ALTER TABLE loan_applications ADD COLUMN IF NOT EXISTS disbursed_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS loan_payments (
  id BIGSERIAL PRIMARY KEY,
  loan_id BIGINT NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  amount_kes NUMERIC(14,2) NOT NULL CHECK (amount_kes > 0),
  method TEXT NOT NULL DEFAULT 'other',
  reference TEXT NOT NULL DEFAULT '',
  paid_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  recorded_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The same M-Pesa/bank reference can only be recorded once.
CREATE UNIQUE INDEX IF NOT EXISTS loan_payments_reference_once ON loan_payments(reference) WHERE reference <> '';
CREATE INDEX IF NOT EXISTS loan_payments_loan ON loan_payments(loan_id);
