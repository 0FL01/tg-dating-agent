package dating

import (
	"bufio"
	"encoding/json"
	"os"
)

const (
	// maxProfileHistoryBytes bounds startup work on large audit files.
	maxProfileHistoryBytes = 128 << 20
)

// restoreProfileDecisions reloads valid past decisions from the local audit
// log into the in-memory cache, so restarts don't re-run the LLM for known
// profiles. Best effort: a missing file, read errors and corrupt lines are
// skipped, never fatal. Only "decision" events with valid decisions are
// loaded; provider failures were never cached and stay uncached.
func restoreProfileDecisions(sm *StateMachine, path string) int {
	if sm == nil || path == "" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var total int64
	restored := 0
	for {
		if total >= maxProfileHistoryBytes {
			return restored
		}
		line, err := reader.ReadBytes('\n')
		total += int64(len(line))
		if len(line) > 0 {
			var rec replyAuditRecord
			if uerr := json.Unmarshal(line, &rec); uerr == nil {
				if rec.Event == "decision" && rec.Decision.Validate() == nil {
					if normalizeProfileTextForCache(rec.ProfileText) != "" || len(rec.PhotoIdentifiers) > 0 {
						sm.SetProfileLLMCache(buildProfileLLMCacheKey(rec.ProfileText, rec.PhotoIdentifiers), rec.Decision)
						restored++
					}
				}
			}
		}
		if err != nil {
			return restored
		}
	}
}
