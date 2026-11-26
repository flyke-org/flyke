package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

type palette struct{ on bool }

func newPalette(w *os.File) palette {
	fi, err := w.Stat()
	return palette{on: err == nil && fi.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""}
}

func (p palette) c(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) dim(s string) string    { return p.c("2", s) }
func (p palette) bold(s string) string   { return p.c("1", s) }
func (p palette) red(s string) string    { return p.c("31", s) }
func (p palette) green(s string) string  { return p.c("32", s) }
func (p palette) yellow(s string) string { return p.c("33", s) }
func (p palette) cyan(s string) string   { return p.c("36", s) }

func (p palette) phase(ph string) string {
	switch ph {
	case "Succeeded":
		return p.green(ph)
	case "Failed", "TimedOut":
		return p.red(ph)
	case "Paused", "Compensating", "Cancelled", "Queued", "Skipped":
		return p.yellow(ph)
	default:
		return p.cyan(ph)
	}
}

// printLog renders one log line: time, origin, level, message, fields.
func printLog(w io.Writer, p palette, e v1.LogEntry, debug bool) {
	if e.Level == "debug" && !debug {
		return
	}
	origin := "flyke"
	switch e.Source {
	case "workflow":
		origin = "workflow"
	case "worker":
		origin = e.Task
		if e.Attempt > 1 {
			origin = fmt.Sprintf("%s#%d", e.Task, e.Attempt)
		}
	}
	level := strings.ToUpper(e.Level)
	if len(level) > 4 {
		level = level[:4]
	}
	switch e.Level {
	case "error":
		level = p.red(level)
	case "warning":
		level = p.yellow(level)
	case "debug":
		level = p.dim(level)
	}
	msg := e.Message
	if e.Source == "system" {
		msg = p.dim(msg)
		origin = p.dim(fmt.Sprintf("%-14s", origin))
	} else {
		origin = p.cyan(fmt.Sprintf("%-14s", origin))
	}
	fields := formatFields(e.Fields)
	if fields != "" {
		fields = " " + p.dim(fields)
	}
	fmt.Fprintf(w, "%s %s %-4s %s%s\n", p.dim(e.Time.Local().Format("15:04:05.000")), origin, level, msg, fields)
}

func formatFields(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	m := map[string]any{}
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		var s string
		switch t := v.(type) {
		case string:
			s = t
			if strings.ContainsAny(s, " \t\n") {
				s = fmt.Sprintf("%q", s)
			}
		default:
			b, _ := json.Marshal(t)
			s = string(b)
		}
		parts = append(parts, k+"="+s)
	}
	return strings.Join(parts, " ")
}

// table prints rows under an upper-case header, in columns padded to the
// widest cell. Widths are measured as the terminal shows them: color escapes
// take no room, wide (CJK, emoji) characters take two cells. (text/tabwriter
// counts escape bytes, so a colored cell pushed its row out of line.)
// Columns whose every value is a number, a ratio like 2/4 or "-" are
// right-aligned, header included.
func table(w io.Writer, header []string, rows [][]string) {
	cols := len(header)
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	cell := func(r []string, i int) string {
		if i < len(r) {
			return strings.Join(strings.Fields(r[i]), " ")
		}
		return ""
	}
	widths := make([]int, cols)
	numeric := make([]bool, cols)
	for i := range cols {
		widths[i] = visibleWidth(cell(header, i))
		numeric[i] = len(rows) > 0
		seen := false
		for _, r := range rows {
			c := cell(r, i)
			widths[i] = max(widths[i], visibleWidth(c))
			if v := stripANSI(c); v != "" && v != "-" {
				seen = true
				numeric[i] = numeric[i] && isNumeric(v)
			}
		}
		numeric[i] = numeric[i] && seen
	}
	// The last column is not padded on the right: no trailing spaces.
	last := cols - 1
	for last > 0 && widths[last] == 0 {
		last--
	}
	line := func(r []string) {
		var b strings.Builder
		for i := 0; i <= last; i++ {
			c := cell(r, i)
			pad := strings.Repeat(" ", widths[i]-visibleWidth(c))
			switch {
			case numeric[i]:
				b.WriteString(pad + c)
			case i < last:
				b.WriteString(c + pad)
			default:
				b.WriteString(c)
			}
			if i < last {
				b.WriteString("   ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(header)
	for _, r := range rows {
		line(r)
	}
}

// stripANSI removes SGR color sequences (ESC [ ... m).
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// visibleWidth is the number of terminal cells s occupies.
func visibleWidth(s string) int {
	n := 0
	for _, r := range stripANSI(s) {
		switch {
		case r == 0x200d || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || (r >= 0xfe00 && r <= 0xfe0f):
			// zero-width joiners, combining marks, variation selectors
		case isWide(r):
			n += 2
		default:
			n++
		}
	}
	return n
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) || (r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) ||
		(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) || (r >= 0x1f900 && r <= 0x1f9ff) || (r >= 0x20000 && r <= 0x3fffd)
}

// isNumeric reports whether a cell reads as a count: 12, 3.5, 2/4.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r == '.' || r == '/' || r == ',':
		default:
			return false
		}
	}
	return digits
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func prettyJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func printJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}
