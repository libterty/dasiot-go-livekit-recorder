package main

import (
	"log"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder"
)

func main() {
	err := recorder.Start()
	if err != nil {
		log.Fatalf("Failed to start recorder: %v", err)
	}
}
