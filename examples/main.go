// Command examples demonstrates the github.com/mustafakarakulak/go-logging
// library: basic logging, the fluent API, masking, integration/queue/job
// context, the HTTP server middleware and the outbound HTTP client transport.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	logging "github.com/mustafakarakulak/go-logging"
	"github.com/mustafakarakulak/go-logging/httpclient"
	"github.com/mustafakarakulak/go-logging/middleware"
)

// PaymentRequest shows struct-tag based masking and extra-field extraction.
type PaymentRequest struct {
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	CreditCard string  `json:"creditCard" mask:"creditcard"`
	Password   string  `json:"password" mask:"hideall"`
	TxnID      string  `json:"transactionId" logextra:"true"`
}

func main() {
	log := logging.New()

	fmt.Println("== 1. Basic structured logging ==")
	log.Info("Invoice created successfully", "invoice_created").
		WithPayload(map[string]any{"invoice_id": "INV-001", "amount": 1000.0, "currency": "TRY"}).
		Log()

	fmt.Println("\n== 2. Fluent API with rich context ==")
	log.Info("Invoice request processed", "invoice_request").
		WithTenant("301009512").
		WithUser("123456").
		WithClientIP("192.168.1.100").
		WithHTTPResult("POST", "/v1/invoices", 201, 145.5).
		WithBytes(5000, 18500).
		WithPayload(map[string]any{"invoice_id": "INV-001", "amount": 1000.0}).
		Log()

	fmt.Println("\n== 3. Masking (struct tags) ==")
	log.Info("Payment processed", "payment_processed").
		WithPayload(PaymentRequest{
			Amount: 100, Currency: "TRY",
			CreditCard: "1111999988883333",
			Password:   "supersecret",
			TxnID:      "TXN-001",
		}).Log()

	fmt.Println("\n== 4. Masking (fluent) ==")
	log.Info("User created", "user_created").
		WithPayload(map[string]any{"tc": "12345678901", "phone": "5551234567"}).
		Mask("tc", logging.ShowFirst2AndLast2).
		Mask("phone", logging.ShowLast2).
		Log()

	fmt.Println("\n== 5. Integration / Queue / Job ==")
	log.Info("Payment processed", "payment_integration").
		WithIntegrationResult("bank-gateway", logging.IntegrationSuccess, 80.5, 0).
		WithPayload(map[string]any{"payment_id": "PAY-001"}).Log()

	log.Info("Message processed", "queue_message").
		WithQueueMessage("invoice_events", "msg-193921", 2, true).Log()

	log.Info("Batch job completed", "batch_job").
		WithJobInfo("invoice_processor", "0 */5 * * * *", "run-20260629-001").
		WithPayload(map[string]any{"processed_count": 150, "failed_count": 2}).Log()

	fmt.Println("\n== 6. Error logging ==")
	log.Error("Failed to create invoice", "invoice_creation_failed").
		WithError(errors.New("insufficient balance")).
		WithPayload(map[string]any{"request_id": "req-123"}).Log()

	fmt.Println("\n== 7. Context propagation ==")
	ctx := logging.WithCorrelationID(context.Background(), "b7f5e0b3b78b4b0fb2df8e5a9c3e22e5")
	log.Info("Handled with trace", "traced_event").Ctx(ctx).Log()

	fmt.Println("\n== 8. HTTP server middleware ==")
	demoMiddleware(log)

	fmt.Println("\n== 9. Outbound HTTP client logging ==")
	demoHTTPClient(log)
}

func demoMiddleware(log *logging.Logger) {
	mw := middleware.New(middleware.Options{
		Logger:          log,
		LogRequestBody:  true,
		LogResponseBody: true,
		IncludePaths:    []string{"/api/*"},
		MaskFieldStrategies: map[string]logging.MaskingStrategy{
			"creditCard": logging.CreditCard,
		},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"invoice_id":"INV-001","status":"created"}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/invoices?page=1",
		strings.NewReader(`{"amount":1000,"creditCard":"1111999988883333"}`))
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func demoHTTPClient(log *logging.Logger) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"paymentId":"PAY-123","status":"success"}`))
	}))
	defer srv.Close()

	client := httpclient.NewClient(nil, httpclient.Options{
		Logger:          log,
		LogRequestBody:  true,
		LogResponseBody: true,
		EventName:       "payment_api_request",
		MaskFieldStrategies: map[string]logging.MaskingStrategy{
			"creditCard": logging.CreditCard,
		},
		LogExtraFields: []string{"transactionId"},
	})

	ctx := logging.WithCorrelationID(context.Background(), "cid-demo-001")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/payments",
		strings.NewReader(`{"amount":100,"creditCard":"1111999988883333","transactionId":"TXN-9"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
