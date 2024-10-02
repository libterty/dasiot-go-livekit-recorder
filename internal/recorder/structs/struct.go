package structs

import (
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"
)

// RecorderConfig config from env
type RecorderConfig struct {
	LiveKitURL     string
	APIKey         string
	APISecret      string
	RoomName       string
	OutputDir      string
	S3Endpoint     string
	S3BucketName   string
	S3AccessKey    string
	S3AccessSecret string
	S3Region       string
}

// Recorder impl
type Recorder interface {
	Start() error
	HandleTrackPublished(*lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
	HandleTrackSubscribed(*webrtc.TrackRemote, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
	HandleTrackUnpublished(*lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
}

// TrackRecorder impl
type TrackRecorder interface {
	Start()
	Stop()
}
