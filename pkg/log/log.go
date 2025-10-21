package log

import (
	"log"
)

func Fatal(v ...any) {
	args := make([]any, 0, len(v)+1)
	args = append(args, "[FATAL]")
	args = append(args, v...)
	log.Println(args...)
}

func Info(v ...any) {
	args := make([]any, 0, len(v)+1)
	args = append(args, "[INFO]")
	args = append(args, v...)
	log.Println(args...)
}
