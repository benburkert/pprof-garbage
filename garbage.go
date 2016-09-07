// Package pprof-garbage writes runtime profiling data in the format expected
// by the pprof visualization tool. The profile shows estimates for garbage
// allocations over a given time duration:
//
//     go tool pprof http://127.0.0.1:6000/debug/pprof/garbage?debug=1
//
// See https://github.com/golang/go/issues/16629 for more details.
package garbage

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

func init() {
	http.Handle("/debug/pprof/garbage", http.HandlerFunc(Garbage))
}

// Garbage returns an HTTP handler that serves the garbage profile.
func Garbage(w http.ResponseWriter, r *http.Request) {
	sec, _ := strconv.Atoi(r.FormValue("seconds"))
	if sec == 0 {
		sec = 30
	}

	debug, _ := strconv.Atoi(r.FormValue("debug"))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()

	WriteGarbageProfile(w, time.Duration(sec)*time.Second, debug != 0)
}

// WriteGarbageProfile writes a pprof-formatted snapshot of the garbage profile
// to w. The profile runs twice as long as duration: the first half is
// calculating the GC period for the duration. The debug parameter enables
// additional output.
func WriteGarbageProfile(w io.Writer, duration time.Duration, debug bool) {
	var garbage, prev []runtime.MemProfileRecord

	if debug {
		w = tabwriter.NewWriter(w, 1, 8, 1, '\t', 0)
	}

	runtime.GC()

	periodGC, numGC := calcPeriod(duration)
	ticker := time.NewTicker(periodGC / 10)
	defer ticker.Stop()

	periodc := ticker.C
	finc := time.After(duration)
	for {
		var fin bool
		if numGC, fin = waitGC(numGC, periodc, finc); fin {
			break
		}

		curr := read()
		if prev != nil {
			for _, cr := range curr {
				if pr, ok := find(prev, cr); ok {
					garbage = update(garbage, pr, cr)
				}
			}
		}
		prev = curr
	}

	var total runtime.MemProfileRecord
	for _, r := range garbage {
		total.AllocBytes += r.AllocBytes
		total.AllocObjects += r.AllocObjects
	}

	fmt.Fprintf(w, "heap profile: %d: %d [%d: %d] @ heap/%d\n",
		total.InUseObjects(), total.InUseBytes(),
		total.AllocObjects, total.AllocBytes,
		2*runtime.MemProfileRate)

	for i := range garbage {
		r := &garbage[i]
		fmt.Fprintf(w, "%d: %d [%d: %d] @",
			r.InUseObjects(), r.InUseBytes(),
			r.AllocObjects, r.AllocBytes)
		for _, pc := range r.Stack() {
			fmt.Fprintf(w, " %#x", pc)
		}
		fmt.Fprintf(w, "\n")
		if debug {
			printStackRecord(w, r.Stack(), false)
		}
	}
}

func calcPeriod(duration time.Duration) (time.Duration, uint32) {
	memstats := new(runtime.MemStats)
	runtime.ReadMemStats(memstats)
	startGC := memstats.NumGC

	time.Sleep(duration)

	runtime.ReadMemStats(memstats)
	return duration / time.Duration(memstats.NumGC-startGC), memstats.NumGC
}

func waitGC(numGC uint32, periodc, finc <-chan time.Time) (uint32, bool) {
	memstats := new(runtime.MemStats)

	i := 0
	for {
		i++
		select {
		case <-finc:
			return numGC, true
		case <-periodc:
			runtime.ReadMemStats(memstats)
			if memstats.NumGC != numGC {
				return memstats.NumGC, false
			}
		}
	}
}

func update(recs []runtime.MemProfileRecord, prev, curr runtime.MemProfileRecord) []runtime.MemProfileRecord {
	garbage := runtime.MemProfileRecord{
		AllocBytes:   min(curr.FreeBytes, prev.AllocBytes),
		AllocObjects: min(curr.FreeObjects, prev.AllocObjects),
		Stack0:       curr.Stack0,
	}

	for i, rec := range recs {
		if sameStack(rec, curr) {
			recs[i].AllocBytes += garbage.AllocBytes
			recs[i].AllocObjects += garbage.AllocObjects

			return recs
		}
	}

	return append(recs, garbage)
}

func find(recs []runtime.MemProfileRecord, want runtime.MemProfileRecord) (runtime.MemProfileRecord, bool) {
	for _, rec := range recs {
		if sameStack(rec, want) {
			return rec, true
		}
	}
	return runtime.MemProfileRecord{}, false
}

func sameStack(r1, r2 runtime.MemProfileRecord) bool {
	if len(r1.Stack0) != len(r2.Stack0) {
		return false
	}
	for i := range r1.Stack0 {
		if r1.Stack0[i] != r2.Stack0[i] {
			return false
		}
	}
	return true
}

func min(a, b int64) int64 {
	if a > b {
		return b
	}
	return a
}

func read() []runtime.MemProfileRecord {
	// Find out how many records there are (MemProfile(nil, true)),
	// allocate that many records, and get the data.
	// There's a race—more records might be added between
	// the two calls—so allocate a few extra records for safety
	// and also try again if we're very unlucky.
	// The loop should only execute one iteration in the common case.
	var p []runtime.MemProfileRecord
	n, ok := runtime.MemProfile(nil, true)
	for {
		// Allocate room for a slightly bigger profile,
		// in case a few more entries have been added
		// since the call to MemProfile.
		p = make([]runtime.MemProfileRecord, n+50)
		n, ok = runtime.MemProfile(p, true)
		if ok {
			p = p[0:n]
			break
		}
		// Profile grew; try again.
	}
	return p
}

// printStackRecord prints the function + source line information
// for a single stack trace.
func printStackRecord(w io.Writer, stk []uintptr, allFrames bool) {
	show := allFrames
	frames := runtime.CallersFrames(stk)
	for {
		frame, more := frames.Next()
		name := frame.Function
		if name == "" {
			show = true
			fmt.Fprintf(w, "#\t%#x\n", frame.PC)
		} else if name != "runtime.goexit" && (show || !strings.HasPrefix(name, "runtime.")) {
			// Hide runtime.goexit and any runtime functions at the beginning.
			// This is useful mainly for allocation traces.
			show = true
			fmt.Fprintf(w, "#\t%#x\t%s+%#x\t%s:%d\n", frame.PC, name, frame.PC-frame.Entry, frame.File, frame.Line)
		}
		if !more {
			break
		}
	}
	if !show {
		// We didn't print anything; do it again,
		// and this time include runtime functions.
		printStackRecord(w, stk, true)
		return
	}
	fmt.Fprintf(w, "\n")
}
