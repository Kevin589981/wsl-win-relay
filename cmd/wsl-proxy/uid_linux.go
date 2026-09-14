//go:build linux

package main

import "os"

func currentUID() int { return os.Getuid() }
