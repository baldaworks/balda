package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAndSummarize(t *testing.T) {
	input := strings.Join([]string{
		`{"Time":"2026-10-22T00:00:00Z","Action":"start","Package":"example/slow"}`,
		`{"Time":"2026-10-22T00:00:01Z","Action":"output","Package":"example/slow","Test":"TestSlow","Output":"=== RUN   TestSlow\n"}`,
		`{"Time":"2026-10-22T00:00:03Z","Action":"pass","Package":"example/slow","Test":"TestSlow","Elapsed":2}`,
		`{"Time":"2026-10-22T00:00:04Z","Action":"pass","Package":"example/slow","Elapsed":4}`,
		`{"Time":"2026-10-22T00:00:04Z","Action":"pass","Package":"example/fast","Elapsed":1}`,
	}, "\n")
	var log bytes.Buffer

	report, err := parse(strings.NewReader(input), &log)
	if err != nil {
		t.Fatalf("parse() error: %v", err)
	}
	if got, want := log.String(), "=== RUN   TestSlow\n"; got != want {
		t.Errorf("plain log = %q, want %q", got, want)
	}
	summary := report.markdown("Example")
	for _, want := range []string{"Observed wall time: 4s", "`example/slow` | 4.000s", "`example/slow/TestSlow` | 2.000s"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestParseRejectsInvalidStreams(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty"},
		{name: "truncated JSON", input: `{"Action":"pass"`},
		{name: "no package completion", input: `{"Action":"run","Package":"example/pkg","Test":"TestOnly"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parse(strings.NewReader(test.input), &bytes.Buffer{}); err == nil {
				t.Fatal("parse() succeeded, want error")
			}
		})
	}
}

func TestSortedUsesDurationThenName(t *testing.T) {
	got := sorted(map[string]float64{"b": 1, "a": 1, "slow": 3})
	want := []string{"slow", "a", "b"}
	for i, item := range got {
		if item.name != want[i] {
			t.Errorf("item %d = %q, want %q", i, item.name, want[i])
		}
	}
}
