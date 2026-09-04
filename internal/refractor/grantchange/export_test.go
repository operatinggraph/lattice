package grantchange

import "time"

// SetClock replaces the clock the content-cycle latch reads. Test-only: the
// latch's interval is a day, and a test that waited one out would not be a test.
func (s *PersonalSweeper) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
