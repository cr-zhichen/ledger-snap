//go:build !linux

package main

// dropRoot only matters in the Linux container; see privdrop_linux.go.
func dropRoot(string) error { return nil }
