# CreditRisk

A small loan-origination app for a Kenyan lender. A customer registers, fills in a profile, and gets an
approve/decline decision from a gradient-boosted model, along with the largest amount they qualify for. If they
accept, the loan goes into a queue for staff, who approve it, mark it as paid out, and record repayments. Customers
get an invoice with the full instalment schedule, as a web page and as a PDF.

It started as a credit-risk modelling exercise on the public Kaggle credit risk dataset and grew a web app around
the model. It is a working prototype, not a lending system. Read [Known limitations](#known-limitations) before you
point it at real money.

## How it fits together

Three processes:

| Service | Language | What it does |
|---|---|---|
| `web` | Go | Auth, profiles, loan applications, pricing, repayment schedules, invoices, staff pages. Server-rendered HTML. |
| `ml` | Python (FastAPI) | Loads the trained XGBoost model and answers `POST /predict`. |
| `db` | Postgres 16 | Everything else. The schema is applied by the Go server on startup. |

```
backend/     Go server (cmd/server), handlers, pricing, statements, store, mailer
             cmd/mockml is a fake model API for working on the app without Python
frontend/    HTML templates, embedded into the Go binary
data/        CreditRisk.csv (training data) and schema.sql
analysis/    analytics.ipynb, the saved model, and serve.py
```

## Running it

### With Docker

```
docker compose up --build
```

Then open http://localhost:8080. Postgres is exposed on `127.0.0.1:5433` so it doesn't fight with a local install.
Anything you want to override (admin emails, SMTP, pricing) can go in a `.env` file next to `docker-compose.yml`.

### Without Docker

You need Go 1.22+, Python 3.10+ and a Postgres you can create a database in.

```
# database
createdb creditrisk        # or whatever you like, then set DATABASE_URL below

# model API (from the repo root)
pip install -r analysis/requirements.txt
uvicorn analysis.serve:app --port 8100

# web server (second terminal, repo root)
export DATABASE_URL="postgres://user:password@localhost:5432/creditrisk?sslmode=disable"
go run ./backend/cmd/server
```

The model API has to be on port 8100 unless you change `ML_API_URL`. If you just want to click around the UI without
the real model, run `go run ./backend/cmd/mockml` instead of uvicorn. It listens on the same port, so run one or the
other.

The server reads a `.env` file from the working directory if there is one. Real environment variables win over it.

While `SMTP_HOST` is empty, password-reset links are written to the server log instead of being emailed.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | |
| `BASE_URL` | `http://localhost:8080` | Used when building reset links |
| `DATABASE_URL` | see `backend/cmd/server/main.go` | Set this yourself. See the note under limitations. |
| `ML_API_URL` | `http://localhost:8100` | |
| `ML_API_KEY` | empty | If set, sent as `X-API-Key` and checked by `serve.py` |
| `COOKIE_SECURE` | `false` | Set `true` behind HTTPS |
| `ADMIN_EMAILS` | empty | Comma-separated. Accounts registered with these emails get the staff pages |
| `COMPANY_NAME` | `Kopa Check` | Printed on invoices |
| `PAYMENT_INSTRUCTIONS` | empty | Shown to customers with an active loan, e.g. a Paybill number |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS`, `SMTP_FROM` | | Mail for password resets |

Read by the model API only:

| Variable | Default | Notes |
|---|---|---|
| `KES_PER_MODEL_UNIT` | `40` | Currency scaling, explained below |
| `BASE_INTEREST_RATE` | `11.0` | Stands in for the `loan_int_rate` feature |
| `MAX_LOAN_PERCENT_INCOME` | `0.40` | Hard cap on loan / annual income |

Pricing (read by the Go server): `PRICING_BASE_ANNUAL_RATE`, `PRICING_RISK_LOADING`, `PRICING_MAX_ANNUAL_RATE`,
`PRICING_MIN_MONTHS`, `PRICING_MAX_MONTHS`, `PRICING_MAX_INSTALLMENT_SHARE`. Defaults are in the pricing section.

## The model

Trained on `data/CreditRisk.csv`: 32,581 rows, 12 columns, with about 22% of loans marked as defaulted. The full
process is in `analysis/analytics.ipynb`: cleaning (median fill for missing rates, rows with no employment length
dropped, IQR capping on outliers, which also removes the impossible ages of 123 and 144 in the raw data), a
logistic regression baseline, random forest and XGBoost with 5-fold CV, randomised hyperparameter search, a
threshold search on the precision-recall curve, sigmoid calibration, and a SHAP pass.

Held-out test set (20%, stratified, 6,517 rows), default class:

| Model | Precision | Recall | F1 |
|---|---|---|---|
| Logistic regression | 0.55 | 0.78 | 0.65 |
| Random forest, tuned | 0.91 | 0.74 | 0.82 |
| XGBoost, tuned | 0.88 | 0.78 | 0.83 |

The shipped model is the calibrated XGBoost (`xgb_model.joblib`). The decision threshold in `best_threshold` is 0.75,
applied to the calibrated default probability. These numbers describe how well the model reproduces labels in this
particular dataset. They say nothing about how it would do on Kenyan borrowers.

### What the form collects vs. what the model needs

The model was trained on 11 features. The customer supplies eight of them: age, annual income, home ownership, years
in current job, loan purpose, requested amount, previous default on file, and credit history length.
`loan_percent_income` is computed. `loan_grade` and `loan_int_rate` are lender-side values the customer doesn't have
yet, so `serve.py` fills in the grade closest to `BASE_INTEREST_RATE` and uses that rate directly.

### Deciding the amount

`/predict` scores the requested amount first. If it fails, it tries 95%, 90%, and so on down to 5% (rounded to the
nearest 100 KES) and returns the largest amount that passes both checks: predicted default probability at or below
the threshold, and loan no more than `MAX_LOAN_PERCENT_INCOME` of annual income. If nothing passes, the customer is
declined.

## Pricing

`backend/internal/pricing` sets the rate, the term and the instalment for each loan. The customer sees all of it
before applying, and the quoted figures are stored on the application.

- **Rate:** `PRICING_BASE_ANNUAL_RATE + PRICING_RISK_LOADING × (1 − confidence)`, rounded to the nearest 0.5% and
  capped at `PRICING_MAX_ANNUAL_RATE`. "Confidence" is the model's probability of repayment.
- **Term:** the shortest number of months, between `PRICING_MIN_MONTHS` and `PRICING_MAX_MONTHS`, where the instalment
  stays within `PRICING_MAX_INSTALLMENT_SHARE` of monthly income. If even the longest term doesn't fit, the customer
  is told the most they can borrow.
- **Instalment:** level payments on a reducing balance, each rounded up to a whole shilling. Total repayable is the
  instalment times the number of months.

| Setting | Default |
|---|---|
| `PRICING_BASE_ANNUAL_RATE` | 14 (% a year) |
| `PRICING_RISK_LOADING` | 20 (extra points at zero confidence) |
| `PRICING_MAX_ANNUAL_RATE` | 30 (% a year) |
| `PRICING_MIN_MONTHS` / `PRICING_MAX_MONTHS` | 1 / 24 |
| `PRICING_MAX_INSTALLMENT_SHARE` | 0.35 |

Example: KES 300,000 at 99% confidence, on an income of KES 150,000 a month, comes out at 14.0% a year over 6 months,
KES 52,062 a month, KES 312,372 in total.

These are placeholders, not anyone's actual rate card. Fees (processing, insurance) aren't modelled.

## Loan lifecycle

1. **Assess.** Once their profile is saved, a customer can run an assessment whenever they like, including while a
   loan is pending or active. It doesn't touch any existing application.
2. **Apply.** After an approved assessment the dashboard offers an application form. The amount starts at the
   qualifying amount and can only be lowered. The server enforces the rules rather than the form: the assessment
   must be the customer's own, approved, and their latest; it can be used for one application only; and a customer
   can have only one pending application at a time.
3. **Review.** Staff open `/admin/loans`, then approve or reject. Applications move through `pending`, `approved`,
   `rejected`, `disbursed` and `cancelled`.
4. **Pay out.** Staff mark the loan as paid out. Repayment dates count from that day.
5. **Repay.** Staff record payments (amount, method, reference, optional date). A payment fills the oldest unpaid
   instalment first. A reference such as an M-Pesa code can only be recorded once, and a payment larger than the
   outstanding balance is refused.

Customers see each instalment marked as paid, part paid, overdue, next due or upcoming, plus their payments, on the
dashboard and on the invoice. `/invoice` shows the latest loan, `/invoice/<id>` an earlier one, and `/statement/<id>`
downloads the PDF (`INV-000001.pdf`).

### Staff accounts

There's no separate staff signup. Put emails in `ADMIN_EMAILS` before starting the server, then register an account
with one of them. Anyone else who requests an `/admin` URL gets a 404.

Payments are entered by hand. There's no M-Pesa integration, so customers can't mark themselves as paid.

## Tests

```
go test ./...
```

Covers the pricing maths, the instalment schedule, payment allocation in the statement package, and a few template
checks. The Python service and the handlers' HTTP paths don't have tests.

## Known limitations

- **Currency scale.** The training data is in dollars (median income about 55,000). Feeding it Kenyan shilling
  amounts directly makes nearly every applicant look very poor, so `serve.py` divides KES amounts by
  `KES_PER_MODEL_UNIT`. The true exchange rate is about 129, which rejects almost everyone, so the default of 40 is a
  fudge that gives sensible-looking results for testing. It has no statistical basis. Real lending needs a model
  trained on local data.
- **Hard-coded database default.** If `DATABASE_URL` isn't set, `backend/cmd/server/main.go` falls back to a
  connection string with a password in it. Remove that default and rotate the password if it was ever real.
- **Secrets in the repo.** `.env` is gitignored but is currently sitting in the project folder. Check it hasn't been
  committed, and don't share the folder as a zip with it inside.
- **Not production-hardened.** Still to do: HTTPS with `COOKIE_SECURE=true`, CSRF tokens, login rate limiting,
  compiled Tailwind CSS instead of the CDN build, real SMTP.
- **Compliance.** Nothing here has been reviewed against the Data Protection Act 2019 or the CBK digital credit
  rules. Get that review before real customers use it.
- **Scikit-learn is pinned** to 1.8.0 in `analysis/requirements.txt` because the joblib file is version-sensitive.
  If you retrain, pin whatever version you trained with.

## Retraining

Open `analysis/analytics.ipynb`, run it top to bottom, and it overwrites `xgb_model.joblib` and `best_threshold` in
`analysis/`. Rebuild the `ml` image afterwards. If you change the feature set, `COLUMNS` and `Applicant` in
`analysis/serve.py` need to match.
