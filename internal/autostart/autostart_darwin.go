// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var loadServiceManagement = sync.OnceValue(func() error {
	// Keep the framework loaded for the lifetime of the process; its ObjC
	// classes and selectors are subsequently used by the service object.
	_, err := purego.Dlopen("/System/Library/Frameworks/ServiceManagement.framework/ServiceManagement", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	return err
})

func mainAppService() (objc.ID, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if !strings.HasSuffix(filepath.Dir(exe), ".app/Contents/MacOS") {
		return 0, errors.New("start at login requires running Kube Workspaces from its installed .app bundle")
	}
	if err := loadServiceManagement(); err != nil {
		return 0, err
	}
	class := objc.GetClass("SMAppService")
	if class == 0 {
		return 0, errors.New("start at login requires macOS 13 or later")
	}
	service := objc.ID(class).Send(objc.RegisterName("mainAppService"))
	if service == 0 {
		return 0, errors.New("macOS did not provide a main app login service")
	}
	return service, nil
}

func serviceStatus(service objc.ID) Status {
	// SMAppServiceStatus: notRegistered=0, enabled=1,
	// requiresApproval=2, notFound=3.
	status := service.Send(objc.RegisterName("status"))
	return Status{Enabled: status == 1 || status == 2, NeedsApproval: status == 2}
}

func withService(f func(objc.ID) (Status, error)) (Status, error) {
	if err := loadServiceManagement(); err != nil {
		return Status{}, err
	}
	// Autoreleased Foundation objects must be drained on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("new"))
	defer pool.Send(objc.RegisterName("drain"))
	service, err := mainAppService()
	if err != nil {
		return Status{}, err
	}
	return f(service)
}

func (Native) Status() (Status, error) {
	return withService(func(service objc.ID) (Status, error) {
		return serviceStatus(service), nil
	})
}

func (Native) SetEnabled(enabled bool) (Status, error) {
	return withService(func(service objc.ID) (Status, error) {
		current := serviceStatus(service)
		if current.Enabled == enabled {
			return current, nil
		}
		selector := "unregisterAndReturnError:"
		if enabled {
			selector = "registerAndReturnError:"
		}
		var nsError objc.ID
		if !objc.Send[bool](service, objc.RegisterName(selector), &nsError) {
			if nsError != 0 {
				description := nsError.Send(objc.RegisterName("localizedDescription"))
				return current, fmt.Errorf("macOS login item: %s", objc.Send[string](description, objc.RegisterName("UTF8String")))
			}
			return current, errors.New("macOS could not change the login item")
		}
		return serviceStatus(service), nil
	})
}
