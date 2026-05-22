package main

import "os"

var isTTY = checkTTY()

func checkTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

const (
	colorDim    = "\033[2m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorReset  = "\033[0m"
)

func colorize(s string, code string) string {
	if !isTTY {
		return s
	}
	return code + s + colorReset
}
