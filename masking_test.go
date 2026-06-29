package logging

import "testing"

func TestMaskStringStrategies(t *testing.T) {
	const in = "12345678932" // 11 chars
	cases := map[MaskingStrategy]string{
		HideAll:            "********",
		ShowFirst1:         "1********",
		ShowLast1:          "**********2",
		ShowFirst2:         "12********",
		ShowLast2:          "*********32",
		ShowFirst1AndLast1: "1********2",
		ShowFirst2AndLast2: "12*******32",
	}
	for strategy, want := range cases {
		if got := MaskString(in, strategy); got != want {
			t.Errorf("MaskString(%q, %q) = %q; want %q", in, strategy, got, want)
		}
	}
}

func TestMaskCreditCard(t *testing.T) {
	got := MaskString("5101521234564582", CreditCard)
	want := "5101 52 **** ** 4582"
	if got != want {
		t.Errorf("CreditCard mask = %q; want %q", got, want)
	}
}

func TestMaskCreditCardShort(t *testing.T) {
	// <= 10 chars: first2 + middle + last2, no grouping.
	got := MaskString("12345678", CreditCard)
	want := "12****78"
	if got != want {
		t.Errorf("short CreditCard mask = %q; want %q", got, want)
	}
}

func TestMaskJSONRecursive(t *testing.T) {
	decoded := map[string]any{
		"amount": 100.0,
		"card":   "1111999988883333",
		"nested": map[string]any{
			"password": "supersecret",
		},
	}
	out := MaskJSON(decoded, map[string]MaskingStrategy{
		"card":     CreditCard,
		"password": HideAll,
	}).(map[string]any)

	if out["card"] == "1111999988883333" {
		t.Errorf("card was not masked: %v", out["card"])
	}
	nested := out["nested"].(map[string]any)
	if nested["password"] != "********" {
		t.Errorf("nested password mask = %v; want ********", nested["password"])
	}
	if out["amount"] != 100.0 {
		t.Errorf("amount should be unchanged, got %v", out["amount"])
	}
}
