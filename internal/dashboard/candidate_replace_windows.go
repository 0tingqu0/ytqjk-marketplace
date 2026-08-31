//go:build windows

package dashboard

import (
	"os"

	"golang.org/x/sys/windows"
)

func replaceFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func candidateSingleLink(file *os.File) bool {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return false
	}
	return information.NumberOfLinks == 1 && information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
