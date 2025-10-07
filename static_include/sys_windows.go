//go:build windows

package vk

import "golang.org/x/sys/windows"

func sys_stringToBytePointer(s string) *byte {
	p, _ := windows.BytePtrFromString(s)
	return p
}

// called once automatically on package init.
func init() {
	libName = "vulkan-1.dll"
	if overrideLibName != "" {
		libName = overrideLibName
	}
	dlHandle = windows.NewLazyDLL(libName)
}
