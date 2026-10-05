//go:build !darwin

package app

import (
	"os"
	"time"
)

func changeTime(fi os.FileInfo) time.Time { return fi.ModTime() }
