//go:build !unix

package main

// foreground is always true without Unix job control.
func foreground() bool { return true }
