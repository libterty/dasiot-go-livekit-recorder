package structs

import (
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"
)

// RecorderConfig 包含錄製器的配置
type RecorderConfig struct {
	LiveKitURL string
	APIKey     string
	APISecret  string
	RoomName   string
}

// Recorder 接口定義錄製器的方法
type Recorder interface {
	Start() error
	HandleTrackPublished(*lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
	HandleTrackSubscribed(*webrtc.TrackRemote, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
	HandleTrackUnpublished(*lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
}

// TrackRecorder 接口定義軌道錄製器的方法
type TrackRecorder interface {
	Start()
	Stop()
}
