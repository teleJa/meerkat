//go:build android && !cgo

package main

func writeAndroidClipboard(string) bool { return false }
