//go:build windows

package proc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type group struct {
	h windows.Handle
}

func newGroup() (*group, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &group{h: h}, nil
}

func (g *group) add(pid int) error {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(p)
	return windows.AssignProcessToJobObject(g.h, p)
}

func (g *group) close() error {
	err := windows.TerminateJobObject(g.h, 1)
	if cerr := windows.CloseHandle(g.h); err == nil {
		err = cerr
	}
	return err
}
