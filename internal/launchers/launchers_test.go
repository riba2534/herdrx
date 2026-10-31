package launchers

import (
	"strings"
	"testing"
)

func TestParseAcceptsAgentJSON(t *testing.T) {
	raw := []byte(`{"launchers":[{"id":"review","name":"审查","command":"codex review"}]}`)
	document, canonical, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Launchers) != 1 || document.Launchers[0].Command != "codex review" {
		t.Fatalf("document = %+v", document)
	}
	if !strings.Contains(string(canonical), `"id":"review"`) {
		t.Fatalf("canonical = %s", canonical)
	}
}

func TestParseRejectsUnsafeOrReservedRows(t *testing.T) {
	cases := []string{
		`{"launchers":[{"id":"claude","name":"X","command":"claude"}]}`,
		`{"launchers":[{"id":"Review","name":"X","command":"codex"}]}`,
		`{"launchers":[{"id":"a","name":"X","command":"echo\nhi"}]}`,
		`{"launchers":[{"id":"a","name":"X","command":"echo\rhi"}]}`,
		`{"launchers":[{"id":"a","name":"多行\n名","command":"echo hi"}]}`,
		`{"launchers":[{"id":"a","name":"X","command":"  "}]}`,
		`{"launchers":[{"id":"a","name":"X","command":"echo hi"},{"id":"a","name":"Y","command":"echo yo"}]}`,
		`{"launchers":[{"id":"a","name":"X","command":"echo hi"}],"extra":true}`,
		`{"launchers":[]}`,
	}
	// The last case is valid and checked separately so a regression that accepts
	// the unsafe ones still fails this test.
	for _, raw := range cases[:len(cases)-1] {
		if _, _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		} else if _, ok := err.(*ValidationError); !ok {
			t.Fatalf("error type for %s: %T", raw, err)
		}
	}
	if _, _, err := Parse([]byte(cases[len(cases)-1])); err != nil {
		t.Fatal(err)
	}
}

func TestParseTrimsAndKeepsUnicode(t *testing.T) {
	document, _, err := Parse([]byte(`{"launchers":[{"id":"hi","name":" 你好 ","command":" echo 你好 "}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if document.Launchers[0].Name != "你好" || document.Launchers[0].Command != "echo 你好" {
		t.Fatalf("%+v", document.Launchers[0])
	}
}

func TestParseLimitsCount(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"launchers":[`)
	for i := 0; i < maxLaunchers+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":"n` + itoa(i) + `","name":"n","command":"true"}`)
	}
	b.WriteString(`]}`)
	if _, _, err := Parse([]byte(b.String())); err == nil {
		t.Fatal("accepted too many launchers")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
