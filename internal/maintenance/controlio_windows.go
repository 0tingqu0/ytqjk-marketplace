//go:build windows

package maintenance

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func directoryIdentity(path string) (string, error) {
	handle, information, err := openWindowsEntry(path, true)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", errors.New("maintenance control path is not a directory")
	}
	index := uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow)
	return fmt.Sprintf("%016x:%016x", uint64(information.VolumeSerialNumber), index), nil
}

func fileHandleIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("maintenance file handle is nil")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return "", err
	}
	index := uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow)
	return fmt.Sprintf("%016x:%016x", uint64(information.VolumeSerialNumber), index), nil
}

func openRootRegularFileNoFollow(directory *os.File, name string, writable bool) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	access := uint32(windows.FILE_GENERIC_READ)
	if writable {
		access |= windows.FILE_GENERIC_WRITE
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	if writable {
		share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle, access, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		share,
		windows.FILE_OPEN,
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
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("maintenance control file is a reparse point or directory")
	}
	return os.NewFile(uintptr(handle), name), nil
}

func openRegularFileNoFollow(path string) (*os.File, error) {
	handle, information, err := openWindowsEntry(path, false)
	if err != nil {
		return nil, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("maintenance control file is not regular")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openLockFileNoFollow(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return nil, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("maintenance lock is a reparse point or directory")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openWindowsEntry(path string, directory bool) (windows.Handle, windows.ByHandleFileInformation, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pathPointer, windows.FILE_READ_ATTRIBUTES|windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0,
	)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		windows.CloseHandle(handle)
		return 0, information, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return 0, information, errors.New("maintenance control entry is a reparse point")
	}
	return handle, information, nil
}

func syncBoundDirectory(*os.Root) error {
	// The temporary file is flushed before the handle-relative replacement.
	// Windows does not expose directory fsync through os.Root.
	return nil
}
