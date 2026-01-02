//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// countProcessesByExeBasename returns number of running processes whose executable basename matches exeName (case-insensitive).
func countProcessesByExeBasename(exeName string) (int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	if err := windows.Process32First(snap, &pe); err != nil {
		return 0, err
	}

	want := strings.ToLower(exeName)
	count := 0
	for {
		name := windows.UTF16ToString(pe.ExeFile[:])
		name = strings.ToLower(filepath.Base(name))
		if name == want {
			count++
		}

		err = windows.Process32Next(snap, &pe)
		if err != nil {
			if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_NO_MORE_FILES {
				break
			}
			return count, err
		}
	}
	return count, nil
}
