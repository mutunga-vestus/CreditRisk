// Stand-in for your real model service so you can test the app end to end.
// Run: go run ./backend/cmd/mockml   (listens on :8100, the same port as the real model API, so run only one of them)
package main

import (
	"encoding/json"
	"log"
	"net/http"

	"loanscore/backend/internal/ml"
)

func main() {
	http.HandleFunc("POST /predict", func(w http.ResponseWriter, r *http.Request) {
		var in ml.Request
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		monthly := in.AnnualIncomeKES / 12
		p := 0.4 + monthly/200000 + in.EmploymentYears*0.02 + float64(in.CreditHistoryYears)*0.01
		if in.DefaultOnFile == "Y" {
			p -= 0.25
		}
		if p > 0.99 {
			p = 0.99
		}
		if p < 0.01 {
			p = 0.01
		}
		out := ml.Response{Approved: p >= 0.55, Probability: p}
		if out.Approved {
			out.AmountKES = float64(int(monthly*3/1000)) * 1000
			if out.AmountKES > in.RequestedAmountKES && in.RequestedAmountKES > 0 {
				out.AmountKES = in.RequestedAmountKES
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	log.Println("mock ML API on :8100")
	log.Fatal(http.ListenAndServe(":8100", nil))
}
