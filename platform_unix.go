//go:build !windows

package main

func platformStartProcess(s *Service) error {
	return s.cmd.Start()
}

func platformCleanup(s *Service) {}
