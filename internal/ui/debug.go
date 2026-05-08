package ui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Optional state-dump for diagnosing the duplicate-row issue. Activated by
// setting GDUI_DEBUG=<path> in the environment before launching gdui. Every
// row-list rebuild appends a timestamped snapshot of (mode, cursor, sections,
// flattened rows) to that file. Off by default — zero overhead when unset.

var (
	dbgOnce sync.Once
	dbgF    *os.File
	dbgMu   sync.Mutex
	dbgN    int
	dbgTag  string
)

func debugTag(t string) {
	if dbgF == nil {
		return
	}
	dbgMu.Lock()
	dbgTag = t
	dbgMu.Unlock()
}

// debugDumpFrame writes the model's full rendered View() (including escape
// sequences) to the debug file. Activated by GDUI_DEBUG_DUMP=1 alongside
// GDUI_DEBUG=<path>. Use sparingly — frames are large.
func debugDumpFrame(m *Model) {
	if dbgF == nil || os.Getenv("GDUI_DEBUG_DUMP") != "1" {
		return
	}
	if !m.ready {
		return
	}
	v := m.View()
	dbgMu.Lock()
	defer dbgMu.Unlock()
	fmt.Fprintf(dbgF, "\n--- frame #%d (raw %d bytes, %d \\n) ---\n", dbgN, len(v), countNL(v))
	fmt.Fprintf(dbgF, "%q\n", v)
}

func countNL(s string) int {
	n := 0
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

func debugDumpRows(m *Model) {
	dbgOnce.Do(func() {
		path := os.Getenv("GDUI_DEBUG")
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return
		}
		dbgF = f
		fmt.Fprintf(dbgF, "# gdui debug log started %s\n", time.Now().Format(time.RFC3339))
	})
	if dbgF == nil {
		return
	}
	dbgMu.Lock()
	defer dbgMu.Unlock()
	dbgN++
	fmt.Fprintf(dbgF, "\n=== refresh #%d  tag=%s  mode=%v cursor=%d activeWT=%d sections=%d rows=%d ===\n",
		dbgN, dbgTag, m.mode, m.cursor, m.activeWT, len(m.sections), len(m.rows))
	dbgTag = ""
	for i, s := range m.sections {
		fmt.Fprintf(dbgF, " section[%d] root=%s expanded=%v files=%d firstLoadDone=%v err=%v\n",
			i, s.WT.Root, s.Expanded, len(s.Files), s.firstLoadDone, s.LoadErr)
	}
	for i, r := range m.rows {
		marker := "  "
		if i == m.cursor {
			marker = "->"
		}
		switch row := r.(type) {
		case headerRow:
			fmt.Fprintf(dbgF, " %s row[%d] HEADER section=%d\n", marker, i, row.sectionIdx)
		case treeRow:
			n := row.node
			if n == nil {
				fmt.Fprintf(dbgF, " %s row[%d] TREE  section=%d node=nil\n", marker, i, row.sectionIdx)
				continue
			}
			fmt.Fprintf(dbgF, " %s row[%d] TREE  section=%d path=%q name=%q isDir=%v exp=%v load=%v hunks=%d ptr=%p\n",
				marker, i, row.sectionIdx, n.Path, n.Name, n.IsDir, n.Expanded, n.Loading, len(n.Hunks), n)
		default:
			fmt.Fprintf(dbgF, " %s row[%d] OTHER %T\n", marker, i, r)
		}
	}
	_ = dbgF.Sync()
}
