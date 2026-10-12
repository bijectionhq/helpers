package command_test

import (
	"concave/internal/testrepo"
	"strings"
	"testing"
)

func TestTimingsCoverSuccessAndFailureWithoutPollutingJSON(t *testing.T) {
	fixture := testrepo.New(t)
	for _, args := range [][]string{{"list"}, {"exception", "refresh", "missing"}, {"surface", "snapshot", "../unsafe.ts"}} {
		_, output, stderr := run(fixture, append(args, "--json", "--timings")...)
		if strings.Contains(output, "elapsed=") || strings.Count(stderr, "elapsed=") != 1 || !strings.Contains(stderr, "git_processes=") || !strings.Contains(stderr, "source_reads=") {
			t.Fatal(args, output, stderr)
		}
	}
}
