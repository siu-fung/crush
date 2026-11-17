package shell

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLineRing_KeepsLastLines(t *testing.T) {
	t.Parallel()

	r := newLineRing(JobTailLines)
	for i := range JobTailLines * 2 {
		fmt.Fprintf(r, "line %d\n", i)
	}

	lines := strings.Split(r.String(), "\n")
	require.Len(t, lines, JobTailLines)
	require.Equal(t, "line 100", lines[0], "the oldest lines are dropped first")
	require.Equal(t, "line 199", lines[len(lines)-1])
	require.Equal(t, JobTailLines, r.Len())
}

func TestLineRing_HoldsPartialLine(t *testing.T) {
	t.Parallel()

	r := newLineRing(10)
	fmt.Fprint(r, "half")
	require.Zero(t, r.Len(), "an unterminated line is not complete yet")
	require.Equal(t, "half", r.String())

	fmt.Fprint(r, " done\n")
	require.Equal(t, 1, r.Len())
	require.Equal(t, "half done", r.String())
}

func TestLineRing_TrimsCarriageReturns(t *testing.T) {
	t.Parallel()

	r := newLineRing(10)
	fmt.Fprint(r, "progress 1\r\nprogress 2\r\n")
	require.Equal(t, "progress 1\nprogress 2", r.String())
}

func TestLineRing_NonPositiveMaxKeepsLinesOut(t *testing.T) {
	t.Parallel()

	r := newLineRing(0)
	fmt.Fprint(r, "a\nb\n")
	require.Zero(t, r.Len())
	require.Empty(t, r.String())
}
