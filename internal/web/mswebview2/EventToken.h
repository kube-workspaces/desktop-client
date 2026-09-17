#ifndef WEBVIEW_COMPAT_EVENTTOKEN_H
#define WEBVIEW_COMPAT_EVENTTOKEN_H
#ifdef _WIN32

// Compatibility header for Windows GNU-family toolchains that do not ship
// EventToken.h with the expected casing or do not ship it at all.
//
// WebView2.h (vendored inside webview_go's libs/mswebview2) includes
// "EventToken.h"; the Microsoft WebView2 SDK ships only WebView2.h +
// WebView2EnvironmentOptions.h, so a native Windows build finds the token
// definition in the Windows SDK while MinGW/WebView2 cross-builds do not.
// This shim is the ABI-equivalent definition used by the upstream webview
// project (https://github.com/webview/webview compat/mingw include).
//
// It is injected into webview_go's compile by build-windows-cgo's
// CGO_CXXFLAGS ("-I<repo>/internal/web/mswebview2"); a #cgo directive in
// our own package cannot set another package's flags.

#ifndef __eventtoken_h__

#ifdef __cplusplus
#include <cstdint>
#else
#include <stdint.h>
#endif

typedef struct EventRegistrationToken {
  int64_t value;
} EventRegistrationToken;

#endif // __eventtoken_h__

#endif // _WIN32
#endif // WEBVIEW_COMPAT_EVENTTOKEN_H