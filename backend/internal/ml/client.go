package ml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Request is the feature payload sent to POST {ML_API_URL}/predict.
// Field names must match the Applicant model in analysis/serve.py.
type Request struct {
	Age                int     `json:"age"`
	AnnualIncomeKES    float64 `json:"annual_income_kes"`
	HomeOwnership      string  `json:"home_ownership"`
	EmploymentYears    float64 `json:"employment_years"`
	LoanIntent         string  `json:"loan_intent"`
	RequestedAmountKES float64 `json:"requested_amount_kes"`
	DefaultOnFile      string  `json:"default_on_file"`
	CreditHistoryYears int     `json:"credit_history_years"`
}

type Response struct {
	Approved    bool    `json:"approved"`
	AmountKES   float64 `json:"amount_kes"`
	Probability float64 `json:"probability"`
}

type Client struct {
	BaseURL, APIKey string
	HTTP            *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) Predict(ctx context.Context, in Request) (*Response, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/predict", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ml api returned status %d", res.StatusCode)
	}
	var out Response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
