//go:build !unix

package main

import "os"

// Other platforms support startup loading; local reload requires restarting.
func reloadNotifications() (<-chan os.Signal, func()) { return nil, func() {} }
