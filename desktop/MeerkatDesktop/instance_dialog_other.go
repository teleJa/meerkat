//go:build !darwin && !windows && !android

package main

func showInstanceConflictDialog(_, _ string) {}
