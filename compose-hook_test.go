// dummy test wrapper to be able to use go test
package main

import (
	"os"
	"testing"
)

func TestMain(*testing.T) {
	// turn [cmd -test.arg ... -- a b c] into [cmd a b c]
	for i := range os.Args {
		if os.Args[i] == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	if err := run(); err != nil {
		logf("%v", err)
	}
}
