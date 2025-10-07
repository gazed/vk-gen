//go:build linux

package vk

import "golang.org/x/sys/unix"
import "unsafe"

// #include <stdlib.h>
// #include "dlload.h"
import "C"

// called once automatically on package init.
func init() {
	libName := "libvulkan.so"
	if overrideLibName != "" {
		libName = overrideLibName
	}
	cstr := C.CString(libName)
	dlHandle = C.OpenLibrary(cstr)
	C.free(unsafe.Pointer(cstr))
}

func sys_stringToBytePointer(s string) *byte {
	p, _ := unix.BytePtrFromString(s)
	return p
}
