package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Describe renders a cron expression in plain English, e.g.
// "Every day at 03:00 (Europe/Paris)". Expressions it has no sentence for
// are shown as they are.
func Describe(expr, tz string) string {
	text, wallClock := describe(strings.TrimSpace(expr))
	if !wallClock {
		return text
	}
	if tz == "" {
		tz = "UTC"
	}
	return text + " (" + tz + ")"
}

var descriptors = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// describe returns the sentence and whether it depends on the time zone.
func describe(expr string) (string, bool) {
	if rest, ok := strings.CutPrefix(expr, "@every "); ok {
		if d, err := time.ParseDuration(strings.TrimSpace(rest)); err == nil && d > 0 {
			return "Every " + interval(d), false
		}
		return expr, false
	}
	if std, ok := descriptors[expr]; ok {
		expr = std
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return expr, true
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	m, mOK := number(minute, 0, 59)
	h, hOK := number(hour, 0, 23)
	at := fmt.Sprintf("%02d:%02d", h, m)

	switch {
	case dom == "*" && month == "*" && dow == "*":
		switch {
		case minute == "*" && hour == "*":
			return "Every minute", false
		case hour == "*":
			if n, ok := step(minute); ok {
				return "Every " + plural(n, "minute"), false
			}
			if mOK && m == 0 {
				return "Every hour", false
			}
			if mOK {
				return fmt.Sprintf("Every hour at minute %d", m), false
			}
		case mOK && hOK:
			return "Every day at " + at, true
		case mOK:
			if n, ok := step(hour); ok {
				text := "Every " + plural(n, "hour")
				if m != 0 {
					text += fmt.Sprintf(" at minute %d", m)
				}
				return text, true
			}
		}
	case mOK && hOK && dom == "*" && month == "*":
		if days, ok := weekdays(dow); ok {
			return days + " at " + at, true
		}
	case mOK && hOK && month == "*" && dow == "*":
		if d, ok := number(dom, 1, 31); ok {
			return fmt.Sprintf("On day %d of every month at %s", d, at), true
		}
	case mOK && hOK && dow == "*":
		d, dOK := number(dom, 1, 31)
		mo, moOK := monthNumber(month)
		if dOK && moOK {
			return fmt.Sprintf("Every year on %s %d at %s", time.Month(mo), d, at), true
		}
	}
	return fmt.Sprintf("Cron %q", expr), true
}

func number(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= lo && n <= hi
}

// step parses "*/N".
func step(s string) (int, bool) {
	rest, ok := strings.CutPrefix(s, "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

func plural(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func interval(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return plural(int(d/time.Minute), "minute")
	case d%time.Second == 0:
		return plural(int(d/time.Second), "second")
	}
	return d.String()
}

var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

func monthNumber(s string) (int, bool) {
	if n, ok := monthNames[strings.ToLower(s)]; ok {
		return n, true
	}
	return number(s, 1, 12)
}

func weekday(s string) (time.Weekday, bool) {
	if n, ok := dayNames[strings.ToLower(s)]; ok {
		return time.Weekday(n), true
	}
	n, ok := number(s, 0, 7)
	return time.Weekday(n % 7), ok
}

// weekdays renders a day-of-week field: "1" → "Every Monday", "1-5" →
// "Monday to Friday", "1,3,5" → "Every Monday, Wednesday and Friday".
func weekdays(field string) (string, bool) {
	if from, to, ok := strings.Cut(field, "-"); ok {
		a, aOK := weekday(from)
		b, bOK := weekday(to)
		if !aOK || !bOK {
			return "", false
		}
		return fmt.Sprintf("%s to %s", a, b), true
	}
	var names []string
	for _, part := range strings.Split(field, ",") {
		d, ok := weekday(part)
		if !ok {
			return "", false
		}
		names = append(names, d.String())
	}
	if len(names) == 1 {
		return "Every " + names[0], true
	}
	return "Every " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1], true
}
