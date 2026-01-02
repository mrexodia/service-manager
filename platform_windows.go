//go:build windows

package main

import (
	"fmt"

	winjob "github.com/kolesnikovae/go-winjob"
)

// platformStartProcess initializes Windows-specific process tracking before start.
func platformStartProcess(s *Service) error {
	job, err := winjob.Create("service-manager-"+s.Config.Name,
		winjob.WithKillOnJobClose(),
		winjob.WithBreakawayOK(),
	)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}

	if err := winjob.StartInJobObject(s.cmd, job); err != nil {
		_ = job.Close()
		return fmt.Errorf("start in job: %w", err)
	}

	s.winJob = job
	return nil
}

func platformCleanup(s *Service) {
	if job, ok := s.winJob.(*winjob.JobObject); ok && job != nil {
		_ = job.Close()
		s.winJob = nil
	}
}
