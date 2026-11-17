package shell

import (
	"strings"
	"sync"
)

// JobTailLines is how much of a background job's output is kept as lines
// for live inspection. Watching a build means watching its tail, and a
// fixed line budget keeps every read cheap no matter how much the job
// prints. The full byte stream stays available through the shell's
// capped buffers for tool output and spill files.
const JobTailLines = 100

// lineRing keeps the last max complete lines of a stream, in memory. It is
// safe for concurrent use. A trailing line without its newline is held
// back as the pending partial and completed by a later write, so a job
// that prints a progress bar does not fill the ring with fragments.
type lineRing struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial string
}

func newLineRing(max int) *lineRing {
	return &lineRing{max: max}
}

// Write appends to the ring, splitting p into lines. It always reports a
// full write: the ring is a view, never the source of truth.
func (r *lineRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	text := r.partial + string(p)
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		r.push(strings.TrimSuffix(text[:i], "\r"))
		text = text[i+1:]
	}
	r.partial = text
	return n, nil
}

// push appends one line, dropping the oldest when the ring is full. Callers
// must hold r.mu.
func (r *lineRing) push(line string) {
	if r.max <= 0 {
		return
	}
	if len(r.lines) >= r.max {
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
		return
	}
	r.lines = append(r.lines, line)
}

// String returns the retained lines, oldest first, ending with the pending
// partial line when the stream stopped mid-line.
func (r *lineRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.lines)+1)
	out = append(out, r.lines...)
	if r.partial != "" {
		out = append(out, r.partial)
	}
	return strings.Join(out, "\n")
}

// Len reports how many complete lines the ring holds, excluding the pending
// partial.
func (r *lineRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}
