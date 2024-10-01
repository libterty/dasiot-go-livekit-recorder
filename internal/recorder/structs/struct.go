package structs

import (
	"errors"
	"os"
	"sync"

	"github.com/joho/godotenv"
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

// NewRecorderConfig 從環境變量創建一個新的 RecorderConfig
func NewRecorderConfig() (*RecorderConfig, error) {
	// 加載 .env 文件（如果存在）
	_ = godotenv.Load()

	config := &RecorderConfig{
		LiveKitURL: os.Getenv("LIVEKIT_URL"),
		APIKey:     os.Getenv("LIVEKIT_API_KEY"),
		APISecret:  os.Getenv("LIVEKIT_API_SECRET"),
		RoomName:   os.Getenv("LIVEKIT_ROOM_NAME"),
	}

	// 檢查必要的配置是否已設置
	if config.LiveKitURL == "" || config.APIKey == "" || config.APISecret == "" || config.RoomName == "" {
		return nil, errors.New("missing required configuration")
	}

	return config, nil
}

// Recorder 結構體代表我們的錄製器
type Recorder struct {
	Config RecorderConfig
	Room   *lksdk.Room
	Tracks sync.Map
}

// TrackRecorder 負責錄製單個軌道
type TrackRecorder struct {
	Track               *webrtc.TrackRemote
	Publication         *lksdk.RemoteTrackPublication
	ParticipantIdentity string
	StopChan            chan struct{}
}
