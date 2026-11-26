package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestTableAlignsColoredAndWideCells(t *testing.T) {
	p := palette{on: true}
	var b bytes.Buffer
	table(&b, []string{"RUN", "PHASE", "SLOTS", "NOTE"}, [][]string{
		{"r-1", p.phase("Succeeded"), "2/4", "ok"},
		{"r-22", "Failed", "10/12", "日本 ✓"},
		{"r-333", p.green("Running"), "-", "multi\nline\tcell"},
	})
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d:\n%s", len(lines), b.String())
	}
	// Every column starts at the same visible offset on every line.
	col := func(line string, word string) int { return visibleWidth(line[:strings.Index(line, word)]) }
	phaseAt := col(lines[0], "PHASE")
	for i, w := range []string{"Succeeded", "Failed", "Running"} {
		if got := col(lines[i+1], w); got != phaseAt {
			t.Errorf("row %d: PHASE column at %d, header at %d\n%s", i, got, phaseAt, b.String())
		}
	}
	noteAt := col(lines[0], "NOTE")
	for i, w := range []string{"ok", "日本", "multi line cell"} {
		if got := col(lines[i+1], w); got != noteAt {
			t.Errorf("row %d: NOTE column at %d, header at %d\n%s", i, got, noteAt, b.String())
		}
	}
	// SLOTS is numeric: right-aligned, so every line ends that column at the same place.
	end := func(line, word string) int { return visibleWidth(line[:strings.LastIndex(line, word)]) + visibleWidth(word) }
	slotsEnd := end(lines[0], "SLOTS")
	for i, w := range []string{"2/4", "10/12", "-"} {
		if got := end(lines[i+1], w); got != slotsEnd {
			t.Errorf("row %d: SLOTS ends at %d, header at %d\n%s", i, got, slotsEnd, b.String())
		}
	}
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			t.Errorf("trailing space in %q", l)
		}
	}
}
