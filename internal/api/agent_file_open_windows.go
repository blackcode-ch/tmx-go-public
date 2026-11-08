package api

import (
	"golang.org/x/sys/windows"
	"os"
)

func openAgentFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }

func singleLinkFile(f *os.File) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) == nil && info.NumberOfLinks == 1
}
