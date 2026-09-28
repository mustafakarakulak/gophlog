package gophlog

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fuzz targets for the masking and payload invariants. A plain `go test` runs
// only the seed corpus below; run one with, for example:
//
//	go test -run '^$' -fuzz '^FuzzMaskJSON$' -fuzztime 30s

// fuzzStrategies includes one unknown strategy, which must behave as HideAll.
var fuzzStrategies = []MaskingStrategy{
	HideAll, ShowFirst1, ShowLast1, ShowFirst2, ShowLast2,
	ShowFirst1AndLast1, ShowFirst2AndLast2, CreditCard, "no-such-strategy",
}

// fuzzVisibleLimit is the most runes each strategy may leave unmasked.
var fuzzVisibleLimit = map[MaskingStrategy]int{
	HideAll: 0, ShowFirst1: 1, ShowLast1: 1, ShowFirst2: 2, ShowLast2: 2,
	ShowFirst1AndLast1: 2, ShowFirst2AndLast2: 4, CreditCard: 10, "no-such-strategy": 0,
}

// hidesAll reports whether the strategy must hide a value completely.
func hidesAll(s MaskingStrategy) bool { return fuzzVisibleLimit[s] == 0 }

func FuzzMaskString(f *testing.F) {
	for _, seed := range []string{
		"", "a", "ab", "abcd", "12345678932", "5101521234564582", "4111-1111-1111-1111",
		"4111 1111 1111 1111", "---", "ğüşiöç", "\xff\xfe", strings.Repeat("x", 40), "*", "a b-c_d",
	} {
		for i := range fuzzStrategies {
			f.Add(seed, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, s string, pick uint8) {
		strategy := fuzzStrategies[int(pick)%len(fuzzStrategies)]
		out := MaskString(s, strategy)
		if s == "" {
			if out != "" {
				t.Fatalf("MaskString(%q, %s) = %q; want empty", s, strategy, out)
			}
			return
		}
		visible := 0
		for _, r := range out {
			if r != '*' && (strategy != CreditCard || r != ' ') {
				visible++
			}
		}
		if limit := fuzzVisibleLimit[strategy]; visible > limit {
			t.Fatalf("MaskString(%q, %s) = %q shows %d runes; limit %d", s, strategy, out, visible, limit)
		}
		if hidesAll(strategy) && (len(out) > 8 || strings.Trim(out, "*") != "") {
			t.Fatalf("MaskString(%q, %s) = %q; want 1-8 asterisks only", s, strategy, out)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("MaskString(%q, %s) = %q is not valid UTF-8", s, strategy, out)
		}
	})
}

// fuzzSecret is planted under the targeted key; it must never survive masking.
const fuzzSecret = "ZZ-planted-secret-ZZ"

func FuzzMaskJSON(f *testing.F) {
	f.Add(`{"password":"hunter2","user":"u"}`, "password", uint16(0), uint8(0))
	f.Add(`{"a":[{"Password":{"x":["s3cr3t",1,true,null]}}]}`, "PASSWORD", uint16(3), uint8(7))
	f.Add(`[1,"two",{"token":"t"},[[[]]]]`, "token", uint16(299), uint8(1))
	f.Add(`{"card":"4111111111111111","nested":{"card":{"deep":"4111"}}}`, "card", uint16(0), uint8(7))
	f.Add(`"scalar"`, "k", uint16(10), uint8(4))
	f.Add(`{}`, "", uint16(1), uint8(8))
	f.Add(`{"loop":1,"doc":2,"n":3}`, "LOOP", uint16(2), uint8(0))
	f.Add(strings.Repeat(`{"a":`, 200)+`1`+strings.Repeat(`}`, 200), "a", uint16(50), uint8(5))
	f.Fuzz(func(t *testing.T, doc, key string, depth uint16, pick uint8) {
		if len(doc) > 4096 {
			return // keep the nesting well inside what json.Marshal accepts
		}
		var decoded any
		if json.Unmarshal([]byte(doc), &decoded) != nil {
			return
		}
		if b, _ := json.Marshal(decoded); bytes.Contains(b, []byte(fuzzSecret)) {
			return // the document already carries the secret outside the target
		}
		strategy := fuzzStrategies[int(pick)%len(fuzzStrategies)]

		// Plant the secret under the targeted key, beside the fuzzed document,
		// inside fuzzed nesting — and make the document contain the whole tree,
		// so the walk must also cut a cycle.
		var root any = map[string]any{key: fuzzSecret, "doc": decoded}
		for i := 0; i < int(depth%300); i++ {
			if i%2 == 0 {
				root = map[string]any{"n": root}
			} else {
				root = []any{root}
			}
		}
		if m, ok := decoded.(map[string]any); ok {
			m["loop"] = root
		}

		out := MaskJSON(root, map[string]MaskingStrategy{key: strategy})
		rendered, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("masked output does not marshal: %v", err)
		}
		if bytes.Contains(rendered, []byte(fuzzSecret)) {
			t.Fatalf("targeted value leaked (%s under %q): %s", strategy, key, rendered)
		}
		if hidesAll(strategy) {
			assertTargetsHidden(t, out, strings.ToLower(key))
		}
	})
}

// assertTargetsHidden fails unless every leaf under a key matching target is
// fully hidden. The tree is MaskJSON output, so it is finite.
func assertTargetsHidden(t *testing.T, v any, target string) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if strings.ToLower(k) == target {
				assertLeavesHidden(t, val)
			} else {
				assertTargetsHidden(t, val, target)
			}
		}
	case []any:
		for _, item := range x {
			assertTargetsHidden(t, item, target)
		}
	}
}

func assertLeavesHidden(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for _, val := range x {
			assertLeavesHidden(t, val)
		}
	case []any:
		for _, item := range x {
			assertLeavesHidden(t, item)
		}
	case nil:
	case string:
		if x == cycleMarker || x == depthMarker {
			return
		}
		if len(x) > 8 || strings.Trim(x, "*") != "" {
			t.Fatalf("targeted leaf not hidden: %q", x)
		}
	default:
		t.Fatalf("targeted leaf not masked to a string: %#v", x)
	}
}

// parityDoc exercises the reflective payload walk with shapes that carry no
// mask or logextra tags; every field decodes from JSON, so the fuzzer can fill
// it in.
type parityDoc struct {
	S     string          `json:"s"`
	I     int64           `json:"i,omitempty"`
	U     uint16          // untagged
	F     float64         `json:"f"`
	B     bool            `json:"b,omitempty"`
	P     *string         `json:"p"`
	L     []int           `json:"l"`
	Bytes []byte          `json:"bytes"`
	M     map[string]int  `json:"m"`
	IM    map[int]string  `json:"im"`
	Arr   [2]string       `json:"arr"`
	A     any             `json:"a"`
	N     *parityDoc      `json:"n,omitempty"`
	Kids  []parityDoc     `json:"kids,omitempty"`
	Raw   json.RawMessage `json:"raw,omitempty"`
	Addr  netip.Addr      `json:"addr"`
	T     time.Time       `json:"t"`
	Q     int             `json:"q,string"`
	Skip  string          `json:"-"`
	parityBase
	*parityExtra
}

type parityBase struct {
	E      string `json:"e"`
	Shadow string `json:"s"` // loses to parityDoc.S: shallower wins
}

type parityExtra struct {
	X int `json:"x"`
}

func FuzzPayloadParity(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"s":"x","i":3,"U":4,"f":1.5,"b":true,"p":"pp","l":[1,2],"bytes":"aGk=","m":{"a":1},"im":{"7":"seven"}}`)
	f.Add(`{"arr":["a","b"],"a":{"k":[1,"two",null,{"z":false}]},"n":{"s":"child","n":{"e":"grandchild"}}}`)
	f.Add(`{"kids":[{"s":"k1"},{"x":5}],"raw":{"b": 1, "a": [2]},"addr":"10.0.0.1","t":"2026-01-02T03:04:05Z"}`)
	f.Add(`{"q":"42","e":"embedded","x":9,"Skip":"no","a":"<b>&amp;"}`)
	f.Add(`[{"a":1},[2,[3]],"s",4.5e10,-0,true,null]`)
	f.Fuzz(func(t *testing.T, doc string) {
		var v any
		if json.Unmarshal([]byte(doc), &v) != nil || jsonDepth(v) > maxPayloadDepth/2 {
			return // invalid, or deep enough for the depth guard to cut it
		}
		fuzzParity(t, v)
		var d parityDoc
		if json.Unmarshal([]byte(doc), &d) == nil {
			fuzzParity(t, d)  // not addressable
			fuzzParity(t, &d) // addressable
		}
	})
}

// fuzzParity fails unless processPayload renders v as encoding/json does,
// compared semantically (map key order aside).
func fuzzParity(t *testing.T, v any) {
	t.Helper()
	want, err := json.Marshal(v)
	if err != nil {
		return // encoding/json rejects the value (a time past year 9999, say)
	}
	normalized, _ := processPayload(v)
	got, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("payload does not marshal although the value does: %v", err)
	}
	var wantAny, gotAny any
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantAny, gotAny) {
		t.Fatalf("payload = %s\nencoding/json = %s", got, want)
	}
}

// jsonDepth returns the nesting depth of a decoded JSON value.
func jsonDepth(v any) int {
	deepest := 0
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			deepest = max(deepest, jsonDepth(e))
		}
	case []any:
		for _, e := range x {
			deepest = max(deepest, jsonDepth(e))
		}
	default:
		return 0
	}
	return deepest + 1
}
