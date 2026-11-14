package cli

import (
	"net/http"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// A plan limit is a quota (exit 5) whatever its HTTP status; a plain 403 is
// still a refused credential (exit 3).
func TestExitCodeForPlanLimits(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{&api.Error{Status: http.StatusForbidden, ErrorCode: "plan_limit_exceeded"}, ExitQuota},
		{&api.Error{Status: http.StatusTooManyRequests, ErrorCode: "plan_limit_exceeded"}, ExitQuota},
		{&api.Error{Status: http.StatusTooManyRequests, ErrorCode: "rate_limited"}, ExitQuota},
		{&api.Error{Status: http.StatusForbidden}, ExitAuth},
	} {
		if got := exitCodeFor(c.err); got != c.want {
			t.Errorf("%v: exit %d, want %d", c.err, got, c.want)
		}
	}
}
