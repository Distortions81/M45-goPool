package main

func (s *workerListStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.bestDiffStop != nil {
		close(s.bestDiffStop)
		s.bestDiffWg.Wait()
	}
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}
