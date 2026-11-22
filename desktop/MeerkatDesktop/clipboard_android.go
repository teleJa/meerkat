//go:build android && cgo

package main

/*
#include <jni.h>
int meerkat_write_clipboard(const jchar *text, jsize length);
*/
import "C"

import (
	"unicode/utf16"
	"unsafe"
)

func writeAndroidClipboard(text string) bool {
	units := utf16.Encode([]rune(text))
	length := len(units)
	if length == 0 {
		units = []uint16{0}
	}
	return C.meerkat_write_clipboard((*C.jchar)(unsafe.Pointer(&units[0])), C.jsize(length)) != 0
}
