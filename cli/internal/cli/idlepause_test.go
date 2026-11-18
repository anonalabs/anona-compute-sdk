package cli

import (
	"strings"
	"testing"
)

// A bare number has no unit. It must be refused with a message that asks for
// one, not one that says 15 is "not a whole number of minutes".
func TestParseIdleMinutesBareNumberNeedsUnit(t *testing.T) {
	for _, v := range []string{"15", "90", "1"} {
		_, err := parseIdleMinutes(v)
		if err == nil {
			t.Fatalf("%q: accepted, want a refusal asking for a unit", v)
		}
		msg := err.Error()
		if strings.Contains(msg, "whole number") || !strings.Contains(msg, "unit") || !strings.Contains(msg, v+"m") {
			t.Errorf("%q: message %q should say it needs a unit and suggest %sm", v, msg, v)
		}
	}
	for v, want := range map[string]int{"15m": 15, "1h": 60, "off": 0, "0": 0} {
		if got, err := parseIdleMinutes(v); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", v, got, err, want)
		}
	}
	if _, err := parseIdleMinutes("90s"); err == nil || !strings.Contains(err.Error(), "whole number of minutes") {
		t.Errorf("90s should still be refused as not a whole number of minutes, got %v", err)
	}
}
