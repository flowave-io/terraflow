package log

import (
	"log"
)

func Fatal(v ...any) {
	log.Println("[FATAL]", v...)
}

func Info(v ...any) {
	log.Println("[INFO]", v...)
}
