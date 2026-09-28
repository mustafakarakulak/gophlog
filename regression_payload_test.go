package gophlog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for the v1.1.1 payload and masking fixes.

// --- helpers --------------------------------------------------------------

const (
	// walkBudget is how long one of the pathological walks below may take.
	// The fixed walks finish in well under a millisecond; the headroom is for
	// the race detector on shared CI runners, where they take ~160ms.
	walkBudget = time.Second
	// walkHangTimeout is when a walk is declared runaway. The test fails
	// right away rather than waiting on it, so the suite never locks up.
	walkHangTimeout = 2 * time.Second
)

// runWalk runs fn on its own goroutine and fails the test when fn takes
// longer than walkBudget, or has not returned at all after walkHangTimeout.
func runWalk(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		fn()
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		if took > walkBudget {
			t.Errorf("walk took %v; want under %v", took, walkBudget)
		}
	case <-time.After(walkHangTimeout):
		t.Fatalf("walk did not return within %v (runaway recursion)", walkHangTimeout)
	}
}

// warnRecorder collects what a logger hands to its OnError callback.
type warnRecorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *warnRecorder) record(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *warnRecorder) mentions(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, err := range r.errs {
		if strings.Contains(err.Error(), sub) {
			return true
		}
	}
	return false
}

// decodedPayload decodes the stringified payload of a parsed log line.
func decodedPayload(t *testing.T, line map[string]any) any {
	t.Helper()
	s, ok := line["payload"].(string)
	if !ok {
		t.Fatalf("payload missing or not a string: %v", line["payload"])
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, s)
	}
	return v
}

// --- B1: cycles -------------------------------------------------------------

// cycleParent and cycleChild model a bidirectional ORM relation.
type cycleParent struct {
	Name     string        `json:"name"`
	Children []*cycleChild `json:"children"`
}

type cycleChild struct {
	Name   string       `json:"name"`
	Parent *cycleParent `json:"parent"`
}

// selfPair branches back into itself twice, so a walk without cycle
// detection visits 2^depth nodes before the depth guard stops it.
type selfPair struct{ A, B *selfPair }

func TestPayloadCycleBidirectionalRelation(t *testing.T) {
	p := &cycleParent{Name: "p"}
	p.Children = []*cycleChild{{Name: "c1", Parent: p}, {Name: "c2", Parent: p}, {Name: "c3", Parent: p}}

	var buf bytes.Buffer
	var warns warnRecorder
	log := New(WithWriter(&buf), WithOnError(warns.record))
	runWalk(t, func() { log.Info("m", "e").WithPayload(p).Log() })

	line := parseLine(t, &buf)
	if line["error_type"] != nil {
		t.Errorf("a cycle must not fail the line: error_type=%v", line["error_type"])
	}
	payload := decodedPayload(t, line).(map[string]any)
	children, _ := payload["children"].([]any)
	if len(children) != 3 {
		t.Fatalf("children = %v", payload["children"])
	}
	for i, c := range children {
		child := c.(map[string]any)
		if child["parent"] != cycleMarker {
			t.Errorf("child %d parent = %v; want %q", i, child["parent"], cycleMarker)
		}
	}
	if !warns.mentions("cycle") {
		t.Errorf("OnError should report the cycle, got %v", warns.errs)
	}
}

func TestPayloadCycleFanOut(t *testing.T) {
	n := &selfPair{}
	n.A, n.B = n, n
	var out any
	runWalk(t, func() { out, _ = processPayload(n) })
	want := map[string]any{"A": cycleMarker, "B": cycleMarker}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("payload = %v; want %v", out, want)
	}
}

func TestPayloadCycleThroughSliceAndMap(t *testing.T) {
	s := []any{"x", nil}
	s[1] = s
	m := map[string]any{"k": "v"}
	m["self"] = m

	var gotSlice, gotMap any
	runWalk(t, func() {
		gotSlice, _ = processPayload(s)
		gotMap, _ = processPayload(m)
	})
	if want := []any{"x", cycleMarker}; !reflect.DeepEqual(gotSlice, want) {
		t.Errorf("slice payload = %v; want %v", gotSlice, want)
	}
	if want := map[string]any{"k": "v", "self": cycleMarker}; !reflect.DeepEqual(gotMap, want) {
		t.Errorf("map payload = %v; want %v", gotMap, want)
	}
}

// TestPayloadSharedValueIsNotACycle guards the cycle check against false
// positives: a value reached twice from different branches is rendered in
// full both times, exactly as json.Marshal renders it.
func TestPayloadSharedValueIsNotACycle(t *testing.T) {
	type leaf struct {
		V string `json:"v"`
	}
	type pair struct{ A, B *leaf }
	shared := &leaf{V: "x"}
	payloadEqualsStdlib(t, pair{A: shared, B: shared})

	// A struct and its first field share an address; reaching the field
	// through a pointer while the struct is on the path is not a cycle.
	type inner struct{ V string }
	type outer struct {
		In inner
		P  *inner
	}
	o := &outer{In: inner{V: "v"}}
	o.P = &o.In
	payloadEqualsStdlib(t, o)

	// A sub-slice shares its parent's backing array.
	items := []any{"a", "b", nil}
	items[2] = items[:2]
	payloadEqualsStdlib(t, items)
}

func TestMaskJSONSelfReferenceTerminates(t *testing.T) {
	self := map[string]any{"password": "hunter2", "k": "v"}
	self["self"] = self
	fan := map[string]any{}
	fan["a"], fan["b"] = fan, fan
	list := []any{"x", nil}
	list[1] = list
	// The strategy targets a container that contains itself.
	targeted := map[string]any{"n": "1"}
	targeted["password"] = targeted

	strategies := map[string]MaskingStrategy{"password": HideAll}
	var outSelf, outFan, outList, outTargeted any
	runWalk(t, func() {
		outSelf = MaskJSON(self, strategies)
		outFan = MaskJSON(fan, strategies)
		outList = MaskJSON(list, strategies)
		outTargeted = MaskJSON(targeted, strategies)
	})

	if want := map[string]any{"password": MaskString("hunter2", HideAll), "k": "v", "self": cycleMarker}; !reflect.DeepEqual(outSelf, want) {
		t.Errorf("self = %v; want %v", outSelf, want)
	}
	if want := map[string]any{"a": cycleMarker, "b": cycleMarker}; !reflect.DeepEqual(outFan, want) {
		t.Errorf("fan = %v; want %v", outFan, want)
	}
	if want := []any{"x", cycleMarker}; !reflect.DeepEqual(outList, want) {
		t.Errorf("list = %v; want %v", outList, want)
	}
	if want := map[string]any{"n": "1", "password": cycleMarker}; !reflect.DeepEqual(outTargeted, want) {
		t.Errorf("targeted = %v; want %v", outTargeted, want)
	}
	// The input is never modified.
	if self["password"] != "hunter2" {
		t.Errorf("MaskJSON mutated its input: %v", self["password"])
	}
}

// TestMaskJSONDepthGuard: every document json.Unmarshal accepts is masked in
// full, while a hand-built tree nested past that is cut instead of recursing
// without bound.
func TestMaskJSONDepthGuard(t *testing.T) {
	strategies := map[string]MaskingStrategy{"password": HideAll}

	doc := strings.Repeat(`{"a":`, 500) + `{"password":"hunter2"}` + strings.Repeat(`}`, 500)
	var decoded any
	if err := json.Unmarshal([]byte(doc), &decoded); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(MaskJSON(decoded, strategies))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "hunter2") || strings.Contains(string(out), "max depth") {
		t.Errorf("deep document not masked in full: ...%s", out[len(out)-80:])
	}

	var deep any = map[string]any{"password": "hunter2"}
	for i := 0; i < maxMaskJSONDepth+5; i++ {
		deep = map[string]any{"a": deep}
	}
	var masked any
	runWalk(t, func() { masked = MaskJSON(deep, strategies) })
	// Too deep for json.Marshal on newer toolchains, so descend by hand.
	levels := 0
	for {
		m, ok := masked.(map[string]any)
		if !ok {
			break
		}
		if _, leaked := m["password"]; leaked {
			t.Fatalf("walk reached the leaf at level %d; want it cut first", levels)
		}
		masked = m["a"]
		levels++
	}
	if masked != depthMarker || levels != maxMaskJSONDepth+1 {
		t.Errorf("over-deep tree cut at level %d with %v; want level %d with %q", levels, masked, maxMaskJSONDepth+1, depthMarker)
	}
}

// --- B2: unknown mask tag values -------------------------------------------

type typoMaskTags struct {
	Password string `json:"password" mask:"hide"`
	Token    string `json:"token" mask:"true"`
	Secret   string `json:"secret" mask:"full"`
	Pin      string `json:"pin" mask:"hide_all"`
	Blank    string `json:"blank" mask:""`
	Plain    string `json:"plain"`
}

func TestMaskTagUnknownStrategyFailsClosed(t *testing.T) {
	var buf bytes.Buffer
	var warns warnRecorder
	log := New(WithWriter(&buf), WithOnError(warns.record))
	log.Info("m", "e").WithPayload(typoMaskTags{
		Password: "hunter2", Token: "tok-123", Secret: "s3cr3t", Pin: "4321", Blank: "blank-secret", Plain: "visible",
	}).Log()

	raw := buf.String()
	for _, secret := range []string{"hunter2", "tok-123", "s3cr3t", "4321", "blank-secret"} {
		if strings.Contains(raw, secret) {
			t.Errorf("value %q behind an unknown mask tag leaked: %s", secret, raw)
		}
	}
	line := parseLine(t, &buf)
	payload := decodedPayload(t, line).(map[string]any)
	for field, secret := range map[string]string{"password": "hunter2", "token": "tok-123", "secret": "s3cr3t", "pin": "4321", "blank": "blank-secret"} {
		if want := MaskString(secret, HideAll); payload[field] != want {
			t.Errorf("%s = %v; want %q (hideall)", field, payload[field], want)
		}
	}
	if payload["plain"] != "visible" {
		t.Errorf("untagged field should be untouched: %v", payload["plain"])
	}
	if line["error_type"] != nil {
		t.Errorf("an unknown tag must not fail the line: error_type=%v", line["error_type"])
	}
	if !warns.mentions(`"hide"`) {
		t.Errorf("OnError should name the unknown strategy, got %v", warns.errs)
	}
}

// --- B3: pointer-receiver and text marshalers ------------------------------

// ptrSecret redacts itself through a pointer-receiver MarshalJSON.
type ptrSecret string

func (s *ptrSecret) MarshalJSON() ([]byte, error) { return []byte(`"***"`), nil }

type ptrSecretUser struct {
	Name     string
	Password ptrSecret
}

// textToken redacts itself through a value-receiver MarshalText.
type textToken struct{ Raw string }

func (textToken) MarshalText() ([]byte, error) { return []byte("REDACTED"), nil }

// redactedName is a string kind whose MarshalText hides the raw value.
type redactedName string

func (redactedName) MarshalText() ([]byte, error) { return []byte("REDACTED"), nil }

func TestPayloadHonoursPointerAndTextMarshalers(t *testing.T) {
	// Addressable (behind a pointer, or a slice element): the pointer method runs.
	payloadEqualsStdlib(t, &ptrSecretUser{Name: "u", Password: "hunter2"})
	payloadEqualsStdlib(t, []ptrSecret{"hunter2"})
	// Not addressable: encoding/json cannot call it either and renders by kind.
	payloadEqualsStdlib(t, ptrSecretUser{Name: "u", Password: "plain"})
	payloadEqualsStdlib(t, map[string]ptrSecret{"k": "plain"})

	payloadEqualsStdlib(t, struct{ T textToken }{textToken{Raw: "sk_live_abc"}})
	payloadEqualsStdlib(t, struct{ IP netip.Addr }{netip.MustParseAddr("10.0.0.1")})
	b := &struct{ N big.Int }{}
	b.N.SetInt64(42)
	payloadEqualsStdlib(t, b)

	out, _ := processPayload(&ptrSecretUser{Name: "u", Password: "hunter2"})
	if rendered, _ := json.Marshal(out); strings.Contains(string(rendered), "hunter2") {
		t.Errorf("pointer-receiver MarshalJSON bypassed: %s", rendered)
	}
}

// TestMaskTagMasksMarshalledText: a mask tag on a type that renders itself
// masks what would have been printed, never the raw value behind it.
func TestMaskTagMasksMarshalledText(t *testing.T) {
	type P struct {
		Token textToken    `json:"token" mask:"showlast2"`
		Name  redactedName `json:"name" mask:"showfirst2"`
	}
	out, _ := processPayload(P{Token: textToken{Raw: "sk_live_abc"}, Name: "secretvalue"})
	m := out.(map[string]any)
	if m["token"] != "******ED" {
		t.Errorf("token = %v; want ******ED", m["token"])
	}
	if m["name"] != "RE******" {
		t.Errorf("name = %v; want RE******", m["name"])
	}
}

// --- B4: mask tags outside the payload -------------------------------------

type taggedCard struct {
	Holder string `json:"holder"`
	Card   string `json:"card" mask:"creditcard"`
	CVV    string `json:"cvv" mask:"hideall"`
	Ref    string `json:"ref" logextra:"true"`
}

// cardValuer resolves to a mask-tagged struct through slog.LogValuer.
type cardValuer struct{ c taggedCard }

func (v cardValuer) LogValue() slog.Value { return slog.AnyValue(v.c) }

func TestMaskTagsApplyOutsideThePayload(t *testing.T) {
	const rawCard, maskedCard, rawCVV = "4111111111111111", "4111 11 **** ** 1111", "cvv-secret-9"
	card := taggedCard{Holder: "holder", Card: rawCard, CVV: rawCVV, Ref: "REF-1"}

	cases := map[string]func(l *Logger){
		"extra field":           func(l *Logger) { l.Info("m", "e").WithExtraField("req", card).Log() },
		"extra field pointer":   func(l *Logger) { l.Info("m", "e").WithExtraField("req", &card).Log() },
		"extra slice":           func(l *Logger) { l.Info("m", "e").WithExtra(map[string]any{"reqs": []taggedCard{card}}).Log() },
		"extra map of pointers": func(l *Logger) { l.Info("m", "e").WithExtraField("reqs", map[string]*taggedCard{"a": &card}).Log() },
		"extra nested any":      func(l *Logger) { l.Info("m", "e").WithExtraField("wrap", map[string]any{"in": []any{card}}).Log() },
		"bound extra":           func(l *Logger) { l.With().ExtraField("req", card).Logger().Info("m", "e").Log() },
		"slog any":              func(l *Logger) { NewSlogLogger(l, nil).Info("m", slog.Any("req", card)) },
		"slog group":            func(l *Logger) { NewSlogLogger(l, nil).Info("m", slog.Group("g", slog.Any("req", card))) },
		"slog with attrs":       func(l *Logger) { NewSlogLogger(l, nil).With("req", card).Info("m") },
		"slog log valuer":       func(l *Logger) { NewSlogLogger(l, nil).Info("m", "req", cardValuer{card}) },
		"integration request":   func(l *Logger) { l.Info("m", "e").WithIntegration(&IntegrationInfo{RequestBody: card}).Log() },
		"integration response":  func(l *Logger) { l.Info("m", "e").WithIntegration(&IntegrationInfo{ResponseBody: []any{&card}}).Log() },
		"bound integration body": func(l *Logger) {
			l.With().Integration(&IntegrationInfo{RequestBody: card}).Logger().Info("m", "e").Log()
		},
	}
	for name, logIt := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logIt(New(WithWriter(&buf)))
			line := buf.String()
			if strings.Contains(line, rawCard) || strings.Contains(line, rawCVV) {
				t.Errorf("mask-tagged value leaked: %s", line)
			}
			if !strings.Contains(line, maskedCard) {
				t.Errorf("card should be rendered masked (%q): %s", maskedCard, line)
			}
			// Outside the payload a logextra tag has nowhere to lift the
			// field to, so it stays in place, as encoding/json renders it.
			if !strings.Contains(line, "REF-1") {
				t.Errorf("logextra field should stay in place: %s", line)
			}
		})
	}
}

// TestMaskTaggedExtraLeavesPlainValuesAlone: extras that cannot carry a mask
// tag are neither copied nor walked, and a shared bound map is never mutated.
func TestMaskTaggedExtraLeavesPlainValuesAlone(t *testing.T) {
	plain := map[string]any{
		"s":      "v",
		"n":      1,
		"f":      2.5,
		"ss":     map[string]string{"a": "b"},
		"nested": map[string]any{"x": []any{1, "y", nil}},
		"struct": struct{ A string }{"a"},
		"time":   time.Unix(0, 0),
	}
	got, err := maskTaggedExtra(plain)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(plain).Pointer() {
		t.Error("an extra map without mask tags should be returned as-is")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = maskTaggedExtra(plain) }); allocs != 0 {
		t.Errorf("checking plain extras allocated %v times; want 0", allocs)
	}

	card := taggedCard{Card: "4111111111111111"}
	child := New(WithWriter(&bytes.Buffer{})).With().ExtraField("req", card).Logger()
	child.Info("m", "e").Log()
	if _, still := child.bound.extra["req"].(taggedCard); !still {
		t.Errorf("bound extra was modified: %T", child.bound.extra["req"])
	}
}

// TestMaskTaggedExtraSeesLaterWrites: which extras need the mask-tag check is
// noted as they are added, but a container is always checked at emit time, so
// a tagged value put into it after it was added is still masked.
func TestMaskTaggedExtraSeesLaterWrites(t *testing.T) {
	const rawCard = "4111111111111111"
	card := taggedCard{Card: rawCard}
	cases := map[string]func(l *Logger){
		"entry": func(l *Logger) {
			m := map[string]any{}
			e := l.Info("m", "e").WithExtraField("s", "plain").WithExtra(map[string]any{"ctx": m})
			m["req"] = card
			e.Log()
		},
		"bound": func(l *Logger) {
			m := map[string]any{}
			child := l.With().ExtraField("ctx", m).Logger()
			m["req"] = card
			child.Info("m", "e").Log()
		},
		"slog with attrs": func(l *Logger) {
			m := map[string]any{}
			sl := NewSlogLogger(l, nil).With("ctx", m)
			m["req"] = card
			sl.Info("m", "s", "plain")
		},
	}
	for name, logIt := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logIt(New(WithWriter(&buf)))
			if line := buf.String(); strings.Contains(line, rawCard) {
				t.Errorf("mask-tagged value leaked: %s", line)
			}
		})
	}
}

// TestExtraCycleIsCutNotDropped: a self-referencing extra value is rendered
// with the cycle marker instead of failing the whole line.
func TestExtraCycleIsCutNotDropped(t *testing.T) {
	m := map[string]any{"k": "v"}
	m["self"] = m
	var buf bytes.Buffer
	log := New(WithWriter(&buf))
	runWalk(t, func() { log.Info("m", "e").WithExtraField("graph", m).Log() })

	line := parseLine(t, &buf)
	if line["error_type"] != nil {
		t.Errorf("line should not fail: %v %v", line["error_type"], line["error_message"])
	}
	extra, _ := line["extra"].(map[string]any)
	graph, _ := extra["graph"].(map[string]any)
	if graph["k"] != "v" || graph["self"] != cycleMarker {
		t.Errorf("extra.graph = %v", extra["graph"])
	}
}

// --- B5: nil pointer map keys ----------------------------------------------

// ptrTextKey implements encoding.TextMarshaler on its pointer.
type ptrTextKey struct{ id string }

func (k *ptrTextKey) MarshalText() ([]byte, error) { return []byte("pk-" + k.id), nil }

// valTextKey implements it on its value, so a nil *valTextKey panics if called.
type valTextKey struct{ id string }

func (k valTextKey) MarshalText() ([]byte, error) { return []byte("vk-" + k.id), nil }

func TestPayloadNilPointerMapKey(t *testing.T) {
	payloadEqualsStdlib(t, map[*ptrTextKey]int{nil: 1, {id: "a"}: 2})
	payloadEqualsStdlib(t, map[*valTextKey]int{nil: 1, {id: "b"}: 2})
}

// --- B6: HTML escaping -------------------------------------------------------

// TestPayloadNoHTMLEscape keeps the stringified payload and integration bodies
// consistent with the envelope: <, > and & stay literal.
func TestPayloadNoHTMLEscape(t *testing.T) {
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("m", "e").
		WithPayload(map[string]any{"html": "<b>&</b>"}).
		WithIntegration(&IntegrationInfo{Target: "<t&>", RequestBody: map[string]any{"q": "a<b"}}).
		Log()

	raw := buf.String()
	for _, want := range []string{`<b>&</b>`, `"target":"<t&>"`, `a<b`} {
		if !strings.Contains(raw, want) {
			t.Errorf("%s should appear unescaped: %s", want, raw)
		}
	}
	line := parseLine(t, &buf)
	if p := decodedPayload(t, line).(map[string]any); p["html"] != "<b>&</b>" {
		t.Errorf("payload not equivalent after decoding: %v", p)
	}
}

// --- B8: encoding/json parity ---------------------------------------------

func TestPayloadStringOptionMatchesEncodingJSON(t *testing.T) {
	type S struct {
		ID   int64       `json:"id,string"`
		Ptr  *int        `json:"ptr,string"`
		NilP *int        `json:"nilp,string"`
		F    float64     `json:"f,string"`
		B    bool        `json:"b,string"`
		Str  string      `json:"str,string"`
		U    uint8       `json:"u,string"`
		Num  json.Number `json:"num,string"`
	}
	n := 7
	payloadEqualsStdlib(t, S{ID: 9007199254740993, Ptr: &n, F: 1.5, B: true, Str: `a"b`, U: 3, Num: "12"})

	// On a non-scalar field the option is ignored. Built with reflect.StructOf
	// because staticcheck (rightly) rejects declaring that tag in source.
	nonScalar := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "Other", Type: reflect.TypeOf([]int(nil)), Tag: `json:"other,string"`},
	})).Elem()
	nonScalar.Field(0).Set(reflect.ValueOf([]int{1}))
	payloadEqualsStdlib(t, nonScalar.Interface())

	// The inner encoding keeps <, > and & literal, like the rest of the
	// payload (json.Marshal would escape them twice over).
	out, _ := processPayload(S{Str: "<&>"})
	if got := out.(map[string]any)["str"]; got != `"<&>"` {
		t.Errorf("quoted string = %v; want %q", got, `"<&>"`)
	}

	type Masked struct {
		ID int64 `json:"id,string" mask:"showlast2"`
	}
	out, _ = processPayload(Masked{ID: 1234567})
	if got := out.(map[string]any)["id"]; got != "*****67" {
		t.Errorf("masked ,string field = %v; want *****67", got)
	}
}

type domTagged struct {
	Name string `json:"Name"`
}
type domUntagged struct{ Name string }
type domLeaf struct{ Z string }
type domDeep struct{ domLeaf }
type domShallow struct{ Z string }
type domX struct{ X string }
type domViaA struct{ domX }
type domViaB struct{ domX }

// TestPayloadFieldDominanceMatchesEncodingJSON locks name resolution across
// embedded structs to encoding/json: the shallowest field wins, a tagged field
// beats an untagged one at the same depth, and a remaining tie drops the name.
func TestPayloadFieldDominanceMatchesEncodingJSON(t *testing.T) {
	type tagWins struct {
		domTagged
		domUntagged
	}
	type depthWins struct {
		domDeep
		domShallow
	}
	type directTagWins struct {
		Alias string `json:"Name"`
		Name  string
	}
	type sameTypeTwice struct {
		domViaA
		domViaB
		Kept string
	}
	payloadEqualsStdlib(t, tagWins{domTagged{"tagged"}, domUntagged{"untagged"}})
	payloadEqualsStdlib(t, depthWins{domDeep{domLeaf{"deep"}}, domShallow{"shallow"}})
	payloadEqualsStdlib(t, directTagWins{Alias: "alias", Name: "name"})
	payloadEqualsStdlib(t, sameTypeTwice{domViaA{domX{"a"}}, domViaB{domX{"b"}}, "k"})
}

// --- B9: slog groups never write into a caller's map ----------------------

// TestSlogGroupNeverWritesCallerMap: a group named like an earlier map-valued
// attribute used to be written straight into the caller's map.
func TestSlogGroupNeverWritesCallerMap(t *testing.T) {
	var buf bytes.Buffer
	sl := NewSlogLogger(newTestLogger(&buf), nil)
	user := map[string]any{"x": 1, "nested": map[string]any{"y": 2}}
	snapshot := func() string { b, _ := json.Marshal(user); return string(b) }
	before := snapshot()

	// Bound attribute, then a group of the same name (and one level deeper).
	sl.With("a", user).WithGroup("a").Info("m", "b", 2)
	sl.With("a", user).WithGroup("a").WithGroup("nested").Info("m", "z", 3)
	// Record attribute, then a record group of the same name.
	sl.Info("m", slog.Any("g", user), slog.Group("g", slog.Int("k", 1)))

	if after := snapshot(); after != before {
		t.Fatalf("the caller's map was written into: %s -> %s", before, after)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	wantExtras := []string{
		`{"a":{"b":2,"nested":{"y":2},"x":1}}`,
		`{"a":{"nested":{"y":2,"z":3},"x":1}}`,
		`{"g":{"k":1,"nested":{"y":2},"x":1}}`,
	}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if got, _ := json.Marshal(m["extra"]); string(got) != wantExtras[i] {
			t.Errorf("line %d extra = %s; want %s", i, got, wantExtras[i])
		}
	}
}

// TestSlogGroupCallerMapConcurrent: through WithAttrs the caller's map is
// shared by every Handle call, so writing into it crashed concurrent logging
// with "concurrent map writes".
func TestSlogGroupCallerMapConcurrent(t *testing.T) {
	user := map[string]any{"x": 1}
	sl := NewSlogLogger(New(WithWriter(&lockedWriter{})), nil).With("a", user).WithGroup("a")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sl.Info("m", "g", g, "i", i)
			}
		}(g)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * walkHangTimeout):
		t.Fatal("concurrent slog logging did not finish")
	}
	if len(user) != 1 {
		t.Errorf("the caller's map was written into: %v", user)
	}
}

// TestSlogCallerMapsLeaveAsPlainMaps: attribute maps reach the rest of the
// logger as plain map[string]any values, however they were nested.
func TestSlogCallerMapsLeaveAsPlainMaps(t *testing.T) {
	root := map[string]any{}
	tagged := addAttr(root, nil, slog.Any("a", map[string]any{"x": map[string]any{"y": 1}}))
	tagged = addAttr(root, []string{"a", "x"}, slog.Any("b", map[string]any{"z": 2})) || tagged
	if !tagged {
		t.Fatal("addAttr should report that it stored a caller map")
	}
	plainAttrMaps(root)
	var check func(path string, v any)
	check = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				check(path+"."+k, e)
			}
		case attrMap:
			t.Errorf("%s is still an attrMap", path)
		}
	}
	check("root", root)
}

// lockedWriter is a concurrency-safe sink for log lines.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
