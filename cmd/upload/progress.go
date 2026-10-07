package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Output is plain ASCII on purpose: a Windows console on a legacy code page
// garbles box-drawing and check-mark characters.

const (
	// labelWidth aligns the step labels into a column.
	labelWidth = 10

	barWidth = 24

	// redrawEvery throttles the in-place progress bar on a terminal.
	redrawEvery = 150 * time.Millisecond

	// lineEveryPercent is the step between progress lines when stdout is not
	// a terminal (CI, a file), where redrawing in place would make a mess.
	lineEveryPercent = 10
)

// logStep prints one aligned step line, e.g. "connected  in 0.4s, ...".
func logStep(label, format string, args ...any) {
	fmt.Printf("%-*s %s\n", labelWidth, label, fmt.Sprintf(format, args...))
}

// progress renders upload progress: a bar redrawn in place on a terminal, or
// a line every few percent otherwise.
type progress struct {
	total       int64
	interactive bool
	start       time.Time

	mu          sync.Mutex
	done        int64
	lastDraw    time.Time
	lastPercent int
	drawn       bool
}

func newProgress(total int64) *progress {
	return &progress{
		total:       total,
		interactive: isTerminal(os.Stdout),
		start:       time.Now(),
		lastPercent: -1,
	}
}

// reader counts bytes as the upload reads them from r.
func (p *progress) reader(r io.Reader) io.Reader {
	return &countingReader{r: r, p: p}
}

func (p *progress) add(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.done += int64(n)
	p.render(false)
}

// finish draws the final 100% state and ends the line.
func (p *progress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.done = p.total
	p.render(true)
	if p.interactive && p.drawn {
		fmt.Println()
	}
}

// abort ends an in-place bar so the error that follows starts on a new line.
func (p *progress) abort() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.interactive && p.drawn {
		fmt.Println()
	}
}

func (p *progress) render(force bool) {
	percent := 100
	if p.total > 0 {
		percent = int(p.done * 100 / p.total)
	}

	if p.interactive {
		if !force && time.Since(p.lastDraw) < redrawEvery {
			return
		}
		p.lastDraw = time.Now()
		p.drawn = true
		// \r returns to the line start; the trailing spaces clear a longer
		// previous line.
		fmt.Printf("\r  %s   ", p.line(percent))
		return
	}

	step := percent / lineEveryPercent * lineEveryPercent
	if step == p.lastPercent && !force {
		return
	}
	if step != p.lastPercent {
		p.lastPercent = step
		fmt.Printf("  %s\n", p.line(percent))
	}
}

// line is "[=====>     ]  52%   7.1 / 13.7 MiB   1.2 MiB/s   ETA 6s".
func (p *progress) line(percent int) string {
	filled := percent * barWidth / 100
	bar := strings.Repeat("=", filled)
	if filled < barWidth {
		bar += ">" + strings.Repeat(" ", barWidth-filled-1)
	}

	elapsed := time.Since(p.start).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(p.done) / elapsed
	}

	eta := "--"
	if speed > 0 && p.done < p.total {
		eta = roundDuration(time.Duration(float64(p.total-p.done) / speed * float64(time.Second))).String()
	} else if p.done >= p.total {
		eta = "0s"
	}

	return fmt.Sprintf("[%s] %3d%%  %s / %s  %s/s  ETA %s",
		bar, percent, formatBytes(p.done), formatBytes(p.total), formatBytes(int64(speed)), eta)
}

type countingReader struct {
	r io.Reader
	p *progress
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.p.add(n)
	}
	return n, err
}

// isTerminal reports whether f is an interactive console rather than a pipe
// or a file. Works on Windows consoles as well as Unix terminals.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// formatBytes renders a size as B, KiB or MiB with one decimal.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// roundDuration keeps durations readable: milliseconds below a second, tenths
// of a second below a minute, whole seconds above.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(100 * time.Millisecond)
	default:
		return d.Round(time.Second)
	}
}
