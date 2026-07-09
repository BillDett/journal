package main

import (
	"context"
	"sort"
	"time"
)

// Autosave owns scheduling only; document persistence remains in the document
// service so timing changes cannot accidentally change storage semantics.
func (s *JournalService) StartAutosave(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = s.FlushAll()
				return
			case <-ticker.C:
				for _, id := range s.pendingIDsOlderThan(s.autosaveInterval()) {
					_, _ = s.FlushDocument(id)
				}
			}
		}
	}()
}

func (s *JournalService) autosaveInterval() time.Duration {
	return time.Duration(s.autosaveIntervalMS()) * time.Millisecond
}
func (s *JournalService) pendingIDsOlderThan(age time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-age)
	ids := make([]string, 0, len(s.pending))
	for id, draft := range s.pending {
		if age == 0 || draft.UpdatedAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
