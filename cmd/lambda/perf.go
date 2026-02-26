package main

import (
	"log"
	"time"
)

// timed returns a function that, when called, logs the elapsed time since
// timed() was called. Usage: defer timed("label")()
func timed(label string) func() {
	start := time.Now()
	return func() {
		log.Printf("%s: %dms", label, time.Since(start).Milliseconds())
	}
}
