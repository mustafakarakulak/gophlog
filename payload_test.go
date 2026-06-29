package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type paymentReq struct {
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	CreditCard string  `json:"creditCard" mask:"creditcard"`
	Password   string  `json:"password" mask:"hideall"`
	TxnID      string  `json:"txnId" logextra:"true"`
}

func TestStructTagMaskingAndExtra(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)

	log.Info("payment", "payment_processed").WithTraceID("t").
		WithPayload(paymentReq{
			Amount:     100,
			Currency:   "TRY",
			CreditCard: "1111999988883333",
			Password:   "supersecret",
			TxnID:      "TXN-001",
		}).Log()

	m := parseLine(t, &buf)

	// logextra field must be lifted to extra and removed from payload.
	extra, ok := m["extra"].(map[string]any)
	if !ok {
		t.Fatalf("extra missing, got %T", m["extra"])
	}
	if extra["txnId"] != "TXN-001" {
		t.Errorf("extra txnId = %v", extra["txnId"])
	}

	payloadStr := m["payload"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["txnId"]; present {
		t.Error("txnId should have been moved out of payload")
	}
	if payload["password"] != "********" {
		t.Errorf("password mask = %v", payload["password"])
	}
	if cc, _ := payload["creditCard"].(string); !strings.Contains(cc, "1111") || !strings.Contains(cc, "*") {
		t.Errorf("creditCard mask = %v", payload["creditCard"])
	}
	if payload["amount"] != 100.0 {
		t.Errorf("amount = %v", payload["amount"])
	}
}

func TestProcessPayloadNil(t *testing.T) {
	p, extra := processPayload(nil)
	if p != nil || extra != nil {
		t.Errorf("nil payload should yield nil/nil, got %v/%v", p, extra)
	}
}
