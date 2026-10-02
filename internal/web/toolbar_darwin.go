// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package web

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdint.h>
#include <stdlib.h>
void *kw_cocoa_attach(void*,uintptr_t,const char*,const char*);
void kw_cocoa_detach(void*);
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func attachNativeToolbar(window unsafe.Pointer, token uintptr, labels, details string) (func(), error) {
	l, d := C.CString(labels), C.CString(details)
	defer C.free(unsafe.Pointer(l))
	defer C.free(unsafe.Pointer(d))
	t := C.kw_cocoa_attach(window, C.uintptr_t(token), l, d)
	if t == nil {
		return nil, fmt.Errorf("web toolbar: Cocoa window unavailable")
	}
	return func() { C.kw_cocoa_detach(t) }, nil
}
