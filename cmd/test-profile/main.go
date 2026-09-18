// Command test-profile turns a Go test JSON stream into bounded timing evidence.
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

const maxJSONLine = 16 << 20

type event struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

type result struct {
	packageElapsed time.Duration
	tests          map[string]time.Duration
	failed         []string
	skipped        []string
}

func main() {
	packageName := flag.String("package", "", "exact package import path")
	timeout := flag.Duration("timeout", 0, "declared package timeout")
	top := flag.Int("top", 10, "number of slowest top-level tests to report")
	required := flag.String("require", "", "comma-separated top-level tests that must pass")
	expectedSkips := flag.String("expect-skip", "", "comma-separated top-level tests assigned to explicit separate gates")
	flag.Parse()
	if *packageName == "" || *timeout <= 0 || *top < 1 || *top > 100 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: test-profile --package IMPORT --timeout DURATION [--top N] [--require TEST,...] [--expect-skip TEST,...]")
		os.Exit(2)
	}
	report, err := analyze(os.Stdin, *packageName, *timeout, *top, splitList(*required), splitList(*expectedSkips))
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-profile:", err)
		os.Exit(1)
	}
	fmt.Print(report)
}

func analyze(input io.Reader, packageName string, timeout time.Duration, top int, required, expectedSkips []string) (string, error) {
	if strings.TrimSpace(packageName) != packageName || packageName == "" || timeout <= 0 || top < 1 || top > 100 {
		return "", errors.New("invalid profiling policy")
	}
	result := result{tests: make(map[string]time.Duration)}
	seenPackagePass := false
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxJSONLine)
	for scanner.Scan() {
		var item event
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return "", fmt.Errorf("decode Go test event: %w", err)
		}
		if item.Package != packageName {
			return "", fmt.Errorf("unexpected package %q", item.Package)
		}
		if item.Test == "" {
			switch item.Action {
			case "pass":
				seenPackagePass = true
				result.packageElapsed = seconds(item.Elapsed)
			case "fail":
				result.failed = append(result.failed, packageName)
			}
			continue
		}
		if strings.Contains(item.Test, "/") {
			if item.Action == "skip" {
				result.skipped = append(result.skipped, item.Test)
			}
			continue
		}
		switch item.Action {
		case "pass":
			result.tests[item.Test] = seconds(item.Elapsed)
		case "fail":
			result.failed = append(result.failed, item.Test)
		case "skip":
			result.skipped = append(result.skipped, item.Test)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Go test stream: %w", err)
	}
	if !seenPackagePass || result.packageElapsed <= 0 {
		return "", errors.New("package did not emit a successful terminal event")
	}
	if len(result.failed) > 0 {
		return "", fmt.Errorf("failed tests: %s", strings.Join(result.failed, ", "))
	}
	actualSkips := append([]string(nil), result.skipped...)
	sort.Strings(actualSkips)
	sort.Strings(expectedSkips)
	if strings.Join(actualSkips, "\x00") != strings.Join(expectedSkips, "\x00") {
		return "", fmt.Errorf("skip set mismatch: got [%s], expected explicit separate gates [%s]", strings.Join(actualSkips, ", "), strings.Join(expectedSkips, ", "))
	}
	for _, name := range required {
		if _, ok := result.tests[name]; !ok {
			return "", fmt.Errorf("required top-level test did not pass: %s", name)
		}
	}
	margin := timeout - result.packageElapsed
	if margin <= 0 {
		return "", fmt.Errorf("package elapsed %s exhausted declared timeout %s", result.packageElapsed, timeout)
	}
	type timing struct {
		name    string
		elapsed time.Duration
	}
	timings := make([]timing, 0, len(result.tests))
	for name, elapsed := range result.tests {
		timings = append(timings, timing{name: name, elapsed: elapsed})
	}
	sort.Slice(timings, func(i, j int) bool {
		if timings[i].elapsed == timings[j].elapsed {
			return timings[i].name < timings[j].name
		}
		return timings[i].elapsed > timings[j].elapsed
	})
	if len(timings) > top {
		timings = timings[:top]
	}
	var output strings.Builder
	fmt.Fprintf(&output, "package=%s elapsed=%s timeout=%s margin=%s passed=%d expected_separate_gate_skips=%d\n", packageName, result.packageElapsed, timeout, margin, len(result.tests), len(actualSkips))
	for index, timing := range timings {
		fmt.Fprintf(&output, "%d\t%s\t%s\n", index+1, timing.elapsed, timing.name)
	}
	return output.String(), nil
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second)).Round(time.Millisecond)
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}
