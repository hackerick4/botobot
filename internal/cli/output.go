package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// printer renders command output. The default is JSON lines (one compact JSON
// object per line, DEV_SPEC §12.3); --pretty switches to a human-readable form
// that routines do not use. It buffers stdout so poll can flush before acking
// (DEV_SPEC §12.3 ack timing).
type printer struct {
	w      *bufio.Writer
	pretty bool
}

func newPrinter(out io.Writer, pretty bool) *printer {
	return &printer{w: bufio.NewWriter(out), pretty: pretty}
}

// emit writes one output record. In JSON mode it is a single compact line; in
// pretty mode it is a short multi-line block keyed by "type".
func (p *printer) emit(obj map[string]any) {
	if p.pretty {
		p.emitPretty(obj)
		return
	}
	// Compact, HTML-escaping disabled so text is readable; no trailing spaces.
	b, err := json.Marshal(obj)
	if err != nil {
		// Should not happen for our own maps; fall back to a minimal object.
		fmt.Fprintf(p.w, "{\"type\":\"error\",\"code\":\"encode\"}\n")
		return
	}
	p.w.Write(b)
	p.w.WriteByte('\n')
}

// raw writes a literal line (used for the NO_NEW_MESSAGES sentinel, DEV_SPEC
// §12.3, which must be exactly "NO_NEW_MESSAGES\n" and is not JSON).
func (p *printer) raw(s string) {
	p.w.WriteString(s)
	p.w.WriteByte('\n')
}

// flush drains the buffer. poll calls this before acking so every item is on
// stdout before the server is told to delete it (DEV_SPEC §12.3).
func (p *printer) flush() error { return p.w.Flush() }

// done flushes the buffer and maps a write failure to a cliError (exit 1). Most
// commands end with `return sess.pr.done()`.
func (p *printer) done() *cliError {
	if err := p.flush(); err != nil {
		return generalErrorf("write output: %v", err)
	}
	return nil
}

// emitPretty prints a human-readable block: a "type:" header line followed by
// the remaining fields, sorted, as "  key: value". Nested maps/slices are shown
// as their JSON for brevity.
func (p *printer) emitPretty(obj map[string]any) {
	typ, _ := obj["type"].(string)
	if typ == "" {
		typ = "item"
	}
	fmt.Fprintf(p.w, "%s:\n", typ)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if k == "type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := obj[k]
		switch v.(type) {
		case string, float64, int, int64, bool, nil:
			fmt.Fprintf(p.w, "  %s: %v\n", k, v)
		default:
			b, _ := json.Marshal(v)
			fmt.Fprintf(p.w, "  %s: %s\n", k, b)
		}
	}
}

// writeErrorLine prints the single-line stderr error object (DEV_SPEC §12.3).
// It never includes secret material; callers build cliError with secret-free
// messages.
func writeErrorLine(w io.Writer, e *cliError) {
	obj := map[string]any{"type": "error", "code": e.Code, "message": e.Message}
	if e.RetryAfter > 0 {
		obj["retry_after_sec"] = e.RetryAfter
	}
	b, err := json.Marshal(obj)
	if err != nil {
		fmt.Fprintln(w, `{"type":"error","code":"encode"}`)
		return
	}
	w.Write(b)
	w.Write([]byte{'\n'})
}
