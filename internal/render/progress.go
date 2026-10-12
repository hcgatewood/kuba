package render

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Progress is a terminal line naming the clusters still pending, e.g. "waiting on banana, +3 more (5/10, 4s)".
//
// Past a delay, it shows one faint line: a spinner, the pending clusters that fit, and how many are done and for how
// long. It redraws in place until Stop erases it, so it never mixes with output. A nil *Progress does nothing, e.g.
// off a terminal.
type Progress struct {
	done    chan string
	stopped chan struct{}
}

// StartProgress starts the progress line on w, which must be a terminal, for the named clusters.
func StartProgress(w io.Writer, delay time.Duration, clusters []string) *Progress {
	p := &Progress{done: make(chan string), stopped: make(chan struct{})}
	go func() {
		defer close(p.stopped)
		p.run(w, delay, slices.Clone(clusters))
	}()
	return p
}

// Done marks a cluster finished. It's safe to call concurrently, but not after Stop.
func (p *Progress) Done(cluster string) {
	if p != nil {
		p.done <- cluster
	}
}

// Stop erases the line, if shown, and returns once it's gone, so output can follow.
func (p *Progress) Stop() {
	if p != nil {
		close(p.done)
		<-p.stopped
	}
}

// spinner is the progress line's braille spinner, turning a frame each progressTick.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// progressTick is how often the progress line redraws, so its spinner and clock move even while no cluster finishes.
const progressTick = 100 * time.Millisecond

// run shows the progress line on w once delay passes, redrawing it each tick and as clusters finish, until Stop.
func (p *Progress) run(w io.Writer, delay time.Duration, pending []string) {
	start, total := time.Now(), len(pending)
	redraw := time.NewTimer(delay)
	defer redraw.Stop()
	shown := false
	for {
		select {
		case name, ok := <-p.done:
			if !ok {
				if shown {
					_, _ = fmt.Fprint(w, eraseLine)
				}
				return
			}
			pending = slices.DeleteFunc(pending, func(n string) bool { return n == name })
		case <-redraw.C:
			shown = true
			redraw.Reset(progressTick)
		}
		if shown && len(pending) > 0 {
			// termWidth-1: a line reaching the last column wraps on some terminals, and \r can't take back a wrap
			line := progressLine(pending, total, time.Since(start), termWidth(w)-1)
			_, _ = fmt.Fprint(w, eraseLine+ansi.Style{}.Faint().Styled(line))
		}
	}
}

// eraseLine blanks the line, leaving the cursor at its start.
const eraseLine = "\r" + ansi.EraseEntireLine

// progressLine is the progress line's text, at most width columns: a spinner frame, as many pending clusters as fit,
// then how many are done and for how long.
//
// It always names the first pending cluster, cut short if need be, so a narrow terminal still shows who's slow.
func progressLine(pending []string, total int, elapsed time.Duration, width int) string {
	frame := spinner[int(elapsed/progressTick)%len(spinner)]
	done, took := total-len(pending), elapsed.Truncate(time.Second)
	line := func(shown int) string {
		names := strings.Join(pending[:shown], ", ")
		if rest := len(pending) - shown; rest > 0 {
			names += fmt.Sprintf(", +%d more", rest)
		}
		return fmt.Sprintf("%s waiting on %s (%d/%d, %s)", frame, names, done, total, took)
	}
	shown := len(pending)
	for shown > 1 && ansi.StringWidth(line(shown)) > width {
		shown--
	}
	return ansi.Truncate(line(shown), width, "…")
}

// termWidth is w's width in columns, or 80 when w isn't a terminal, e.g. in tests.
func termWidth(w io.Writer) int {
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}
