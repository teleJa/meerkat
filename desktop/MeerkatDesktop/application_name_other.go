//go:build !windows && !android

package main

func desktopApplicationName() string { return "Meerkat" }

func loadToastProfile() error { return nil }
func registerToastProfile()   {}
