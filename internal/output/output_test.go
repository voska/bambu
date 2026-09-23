package output

import (
	"bytes"
	"strings"
	"testing"
)

func newTest(jsonMode, plain, quiet bool) (*Out, *bytes.Buffer) {
	o := New(jsonMode, plain, quiet, true)
	var buf bytes.Buffer
	o.Stdout = &buf
	o.Stderr = &bytes.Buffer{}
	return o, &buf
}

func TestModes(t *testing.T) {
	v := View{
		Data:  map[string]any{"state": "IDLE"},
		Human: func(h *Human) { h.Line("state %s", "IDLE") },
		Plain: [][]string{{"state"}, {"IDLE"}},
		Quiet: "IDLE",
	}
	cases := []struct {
		name              string
		jsonM, plain, qui bool
		want              string
	}{
		{"human", false, false, false, "state IDLE\n"},
		{"json", true, false, false, "{\n  \"state\": \"IDLE\"\n}\n"},
		{"plain", false, true, false, "state\nIDLE\n"},
		{"quiet", false, false, true, "IDLE\n"},
		{"json wins over quiet", true, false, true, "{\n  \"state\": \"IDLE\"\n}\n"},
	}
	for _, c := range cases {
		o, buf := newTest(c.jsonM, c.plain, c.qui)
		if err := o.Print(v); err != nil {
			t.Fatal(err)
		}
		if buf.String() != c.want {
			t.Errorf("%s: got %q want %q", c.name, buf.String(), c.want)
		}
	}
}

func TestEventNDJSON(t *testing.T) {
	o, buf := newTest(true, false, false)
	_ = o.Event(map[string]int{"layer": 1}, "layer 1")
	_ = o.Event(map[string]int{"layer": 2}, "layer 2")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || lines[1] != `{"layer":2}` {
		t.Fatalf("got %q", buf.String())
	}
}
