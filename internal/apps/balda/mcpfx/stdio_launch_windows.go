package mcpfx

import (
	"errors"
	"os"
	"os/signal"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PROC_THREAD_ATTRIBUTE_JOB_LIST is available since Windows 10. x/sys does
// not name it yet. Assigning the job inside CreateProcess closes the orphan
// window of CreateProcess(CREATE_SUSPENDED) followed by AssignProcessToJobObject.
// https://devblogs.microsoft.com/oldnewthing/20230209-00/?p=107812
const procThreadAttributeJobList = 0x0002000d

func runStdioProcess(directory, executable string, args []string) (int, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 1, err
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return 1, err
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return 1, err
	}
	defer attributes.Delete()
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return 1, err
	}
	// Inherit only duplicated stdio handles. In particular, the child cannot
	// keep the kill-on-close job alive after forced wrapper termination.
	var handles [3]windows.Handle
	for index, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &handles[index], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return 1, err
		}
		defer func() { _ = windows.CloseHandle(handles[index]) }()
	}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return 1, err
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 1, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args...)))
	if err != nil {
		return 1, err
	}
	cwd, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return 1, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	var process windows.ProcessInformation
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, nil, cwd, &startup.StartupInfo, &process); err != nil {
		return 1, err
	}
	defer func() { _ = windows.CloseHandle(process.Process) }()
	_ = windows.CloseHandle(process.Thread)
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	done := make(chan error, 1)
	go func() {
		status, err := windows.WaitForSingleObject(process.Process, windows.INFINITE)
		if err == nil && status != windows.WAIT_OBJECT_0 {
			err = errors.New("MCP stdio wait failed")
		}
		done <- err
	}()
	select {
	case err = <-done:
	case <-interrupts:
		// The child shares the console and receives the same interrupt. Give
		// it a bounded graceful exit before terminating the owned process tree.
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case err = <-done:
		case <-timer.C:
			_ = windows.TerminateJobObject(job, 1)
			err = <-done
		}
	}
	if err != nil {
		return 1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process.Process, &code); err != nil {
		return 1, err
	}
	return int(code), nil
}
