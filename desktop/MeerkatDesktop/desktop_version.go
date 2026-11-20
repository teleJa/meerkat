//go:build !android

package main

// Package builds inject the version from the repository VERSION file.
var appVersion = "0.0.0"

// The independently controlled release public key is injected at build time.
var updatePublicKey string
