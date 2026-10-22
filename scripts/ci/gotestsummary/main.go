package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type event struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

type duration struct {
	name    string
	seconds float64
}

type report struct {
	packages map[string]float64
	tests    map[string]float64
	first    time.Time
	last     time.Time
	events   int
}

func main() {
	var title string
	var outputPath string
	var logPath string
	flag.StringVar(&title, "title", "Go test", "heading used in the Markdown summary")
	flag.StringVar(&outputPath, "output", "", "Markdown summary output path")
	flag.StringVar(&logPath, "log", "", "plain test output path")
	flag.Parse()

	if outputPath == "" || logPath == "" {
		fmt.Fprintln(os.Stderr, "-output and -log are required")
		os.Exit(2)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test log: %v\n", err)
		os.Exit(1)
	}
	report, parseErr := parse(os.Stdin, logFile)
	closeErr := logFile.Close()
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "parse go test JSON: %v\n", parseErr)
		os.Exit(1)
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "close test log: %v\n", closeErr)
		os.Exit(1)
	}

	if err := os.WriteFile(outputPath, []byte(report.markdown(title)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write timing summary: %v\n", err)
		os.Exit(1)
	}
}

func parse(r io.Reader, log io.Writer) (report, error) {
	report := report{
		packages: make(map[string]float64),
		tests:    make(map[string]float64),
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event event
		if err := json.Unmarshal(line, &event); err != nil {
			return report, fmt.Errorf("event %d: %w", report.events+1, err)
		}
		report.events++
		if event.Output != "" {
			if _, err := io.WriteString(log, event.Output); err != nil {
				return report, fmt.Errorf("write plain log: %w", err)
			}
		}
		if !event.Time.IsZero() {
			if report.first.IsZero() || event.Time.Before(report.first) {
				report.first = event.Time
			}
			if report.last.IsZero() || event.Time.After(report.last) {
				report.last = event.Time
			}
		}
		if !isFinal(event.Action) || event.Package == "" {
			continue
		}
		if event.Test == "" {
			report.packages[event.Package] = event.Elapsed
			continue
		}
		report.tests[event.Package+"/"+event.Test] = event.Elapsed
	}
	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("read event stream: %w", err)
	}
	if report.events == 0 {
		return report, errors.New("event stream is empty")
	}
	if len(report.packages) == 0 {
		return report, errors.New("event stream has no final package events")
	}
	return report, nil
}

func isFinal(action string) bool {
	return action == "pass" || action == "fail" || action == "skip"
}

func (r report) markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s timing\n\n", title)
	fmt.Fprintf(&b, "- Events: %d\n", r.events)
	fmt.Fprintf(&b, "- Packages: %d\n", len(r.packages))
	fmt.Fprintf(&b, "- Observed wall time: %s\n\n", r.wallTime().Round(time.Millisecond))
	writeTable(&b, "Slowest packages", sorted(r.packages), 20)
	writeTable(&b, "Slowest tests", sorted(r.tests), 20)
	return b.String()
}

func (r report) wallTime() time.Duration {
	if r.first.IsZero() || r.last.IsZero() || r.last.Before(r.first) {
		return 0
	}
	return r.last.Sub(r.first)
}

func sorted(values map[string]float64) []duration {
	items := make([]duration, 0, len(values))
	for name, seconds := range values {
		items = append(items, duration{name: name, seconds: seconds})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].seconds == items[j].seconds {
			return items[i].name < items[j].name
		}
		return items[i].seconds > items[j].seconds
	})
	return items
}

func writeTable(b *strings.Builder, heading string, items []duration, limit int) {
	fmt.Fprintf(b, "### %s\n\n", heading)
	if len(items) == 0 {
		b.WriteString("No completed entries.\n\n")
		return
	}
	b.WriteString("| Name | Duration |\n| --- | ---: |\n")
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		name := strings.ReplaceAll(item.name, "|", "\\|")
		fmt.Fprintf(b, "| `%s` | %.3fs |\n", name, item.seconds)
	}
	b.WriteString("\n")
}
