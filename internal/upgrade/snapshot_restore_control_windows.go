//go:build windows

package upgrade

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openRestoreDirectoryNoFollow(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer, windows.FILE_READ_ATTRIBUTES|windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err := verifyRestoreWindowsDirectory(handle); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openRestoreDirectoryAtNoFollow(parent *os.File, name string) (*os.File, error) {
	if parent == nil {
		return nil, errors.New("restore parent directory handle is nil")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle, windows.FILE_GENERIC_READ, &attributes,
		&windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_DIRECTORY_FILE|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT,
		0, 0,
	)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			err = status.Errno()
		}
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if err := verifyRestoreWindowsDirectory(handle); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openRestoreRegularAtNoFollow(directory *os.File, name string, writable bool) (*os.File, error) {
	if directory == nil {
		return nil, errors.New("restore directory handle is nil")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(directory.Fd()), ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	access := uint32(windows.FILE_GENERIC_READ)
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	if writable {
		access |= windows.FILE_GENERIC_WRITE
		share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle, access, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, share, windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT,
		0, 0,
	)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			err = status.Errno()
		}
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, errors.Join(errors.New("restore control entry is a reparse point or directory"), file.Close())
	}
	return file, nil
}

func restoreHandleIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("restore control handle is nil")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return "", err
	}
	index := uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow)
	return fmt.Sprintf("%016x:%016x", uint64(information.VolumeSerialNumber), index), nil
}

func verifyRestoreWindowsDirectory(handle windows.Handle) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("restore directory is unsafe")
	}
	return nil
}

func syncRestoreDirectory(*os.File) error {
	// Handle-relative replacement is used and every file is flushed first.
	// Windows does not expose directory fsync through os.Root.
	return nil
}
