"""ML API for the Go backend. Uses your trained model (xgb_model.joblib) and best_threshold.
Run from the project root:  uvicorn analysis.serve:app --port 9000
"""
import os
import warnings
from typing import Literal

import joblib
import pandas as pd
from fastapi import FastAPI, Header, HTTPException
from pydantic import BaseModel, Field

warnings.filterwarnings("ignore")
HERE = os.path.dirname(os.path.abspath(__file__))

model = joblib.load(os.path.join(HERE, "xgb_model.joblib"))
# Applicants are rejected when predicted default probability is above this value.
THRESHOLD = float(joblib.load(os.path.join(HERE, "best_threshold")))

# The model was trained on US-dollar-scale incomes and loans (median income about 55,000), so KES amounts
# are divided by this number before scoring. The real exchange rate is about 129, but that makes almost
# every Kenyan income look very low to the model, so 40 is a placeholder income scale for testing.
# Retrain on Kenyan loan data, or tune this value, before using results to make real lending decisions.
KES_PER_MODEL_UNIT = float(os.getenv("KES_PER_MODEL_UNIT", "40"))
BASE_INTEREST_RATE = float(os.getenv("BASE_INTEREST_RATE", "11.0"))  # % the lender expects to charge
MAX_LOAN_PERCENT_INCOME = float(os.getenv("MAX_LOAN_PERCENT_INCOME", "0.40"))  # loan / annual income
API_KEY = os.getenv("ML_API_KEY", "")

# Median interest rate per grade in the training data; the grade is inferred from the rate.
GRADE_RATES = {"A": 7.49, "B": 10.99, "C": 13.48, "D": 15.31, "E": 16.82, "F": 18.54, "G": 20.16}
GRADE = min(GRADE_RATES, key=lambda g: abs(GRADE_RATES[g] - BASE_INTEREST_RATE))

COLUMNS = ["person_age", "person_income", "person_home_ownership", "person_emp_length",
           "loan_intent", "loan_grade", "loan_amnt", "loan_int_rate", "loan_percent_income",
           "cb_person_default_on_file", "cb_person_cred_hist_length"]

app = FastAPI()


class Applicant(BaseModel):
    age: int = Field(ge=18, le=100)
    annual_income_kes: float = Field(gt=0)
    home_ownership: Literal["RENT", "OWN", "MORTGAGE", "OTHER"]
    employment_years: float = Field(ge=0, le=60)
    loan_intent: Literal["PERSONAL", "EDUCATION", "MEDICAL", "VENTURE", "HOMEIMPROVEMENT", "DEBTCONSOLIDATION"]
    requested_amount_kes: float = Field(gt=0)
    default_on_file: Literal["Y", "N"]
    credit_history_years: int = Field(ge=0, le=60)


def default_probabilities(a: Applicant, amounts_kes: list[float]) -> list[float]:
    income = a.annual_income_kes / KES_PER_MODEL_UNIT
    rows = []
    for amt in amounts_kes:
        loan = amt / KES_PER_MODEL_UNIT
        rows.append([a.age, income, a.home_ownership, a.employment_years, a.loan_intent, GRADE,
                     loan, BASE_INTEREST_RATE, loan / income, a.default_on_file, a.credit_history_years])
    return model.predict_proba(pd.DataFrame(rows, columns=COLUMNS))[:, 1].tolist()


@app.post("/predict")
def predict(a: Applicant, x_api_key: str = Header(default="")):
    if API_KEY and x_api_key != API_KEY:
        raise HTTPException(401, "bad api key")
    # Try the requested amount, then 95%, 90% ... 10% of it. Offer the largest amount that passes.
    steps = [round(a.requested_amount_kes * f / 100) * 100 for f in [1 - i * 0.05 for i in range(19)]]
    steps = [s for s in steps if s > 0]
    probs = default_probabilities(a, steps)
    for amount, p_default in zip(steps, probs):
        if p_default <= THRESHOLD and amount / a.annual_income_kes <= MAX_LOAN_PERCENT_INCOME:
            return {"approved": True, "amount_kes": amount, "probability": 1 - p_default}
    return {"approved": False, "amount_kes": 0, "probability": 1 - probs[0] if probs else 0.0}
