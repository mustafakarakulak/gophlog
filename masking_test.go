package gophlog

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
	// Too short to be a real card (12-19 digits), so it is hidden entirely
	// rather than partially leaked.
	got := MaskString("12345678", CreditCard)
	want := "********"
	if got != want {
		t.Errorf("short CreditCard mask = %q; want %q", got, want)
	}
	// A 12-digit card still gets BIN + last four.
	if got := MaskString("123456789012", CreditCard); got != "1234 56 ** 9012" {
		t.Errorf("12-digit CreditCard mask = %q", got)
	}

	// 11 digits is below the minimum real card length (12); BIN+last4 would
	// reveal 10 of its 11 characters, so it must be hidden entirely.
	if got := MaskString("12345678901", CreditCard); got != "********" {
		t.Errorf("11-digit CreditCard mask = %q; want fully hidden", got)
	}
}

// TestMaskStringFailClosedShort locks in that no strategy leaks a value that is
// too short for it to hide anything.
func TestMaskStringFailClosedShort(t *testing.T) {
	cases := []struct {
		in       string
		strategy MaskingStrategy
		want     string
	}{
		{"5", ShowLast1, "*"},
		{"5", ShowLast2, "*"},
		{"42", ShowLast2, "**"},
		{"5", ShowFirst1, "*"},
		{"42", ShowFirst2, "**"},
		{"42", ShowFirst1AndLast1, "**"},
		{"1234", ShowFirst2AndLast2, "****"},
		{"123", CreditCard, "***"},
		// Long enough to hide something: the strategy applies normally.
		{"42", ShowLast1, "*2"},
		{"123", ShowLast2, "*23"},
	}
	for _, c := range cases {
		if got := MaskString(c.in, c.strategy); got != c.want {
			t.Errorf("MaskString(%q, %q) = %q; want %q", c.in, c.strategy, got, c.want)
		}
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
