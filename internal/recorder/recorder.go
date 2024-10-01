package recorder

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/joho/godotenv"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder/structs"
)

// recorderImpl 實現 structs.Recorder 接口
type recorderImpl struct {
	config structs.RecorderConfig
	room   *lksdk.Room
	tracks sync.Map
}

// trackRecorderImpl 實現 structs.TrackRecorder 接口
type trackRecorderImpl struct {
	track               *webrtc.TrackRemote
	publication         *lksdk.RemoteTrackPublication
	participantIdentity string
	stopChan            chan struct{}
}

// NewRecorderConfig 從環境變量創建一個新的 RecorderConfig
func NewRecorderConfig() (*structs.RecorderConfig, error) {
	// 加載 .env 文件（如果存在）
	_ = godotenv.Load()

	config := &structs.RecorderConfig{
		LiveKitURL: os.Getenv("LIVEKIT_URL"),
		APIKey:     os.Getenv("LIVEKIT_API_KEY"),
		APISecret:  os.Getenv("LIVEKIT_API_SECRET"),
		RoomName:   os.Getenv("LIVEKIT_ROOM_NAME"),
	}

	// 檢查必要的配置是否已設置
	if config.LiveKitURL == "" || config.APIKey == "" || config.APISecret == "" || config.RoomName == "" {
		return nil, fmt.Errorf("missing required configuration")
	}

	return config, nil
}

// NewRecorder 創建一個新的錄製器實例
func NewRecorder(config structs.RecorderConfig) structs.Recorder {
	return &recorderImpl{
		config: config,
	}
}

// Start 開始錄製過程
func (r *recorderImpl) Start() error {
	log.Println("Starting the recorder...")

	room, err := lksdk.ConnectToRoom(r.config.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              r.config.APIKey,
		APISecret:           r.config.APISecret,
		RoomName:            r.config.RoomName,
		ParticipantIdentity: "recorder",
		ParticipantName:     "Recorder Bot",
	}, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished:   r.HandleTrackPublished,
			OnTrackUnpublished: r.HandleTrackUnpublished,
			OnTrackSubscribed:  r.HandleTrackSubscribed,
		},
	})

	if err != nil {
		return fmt.Errorf("failed to connect to room: %v", err)
	}

	r.room = room
	log.Println("Connected to room:", r.config.RoomName)

	// 等待中斷信號
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down recorder...")
	r.room.Disconnect()

	return nil
}

func (r *recorderImpl) HandleTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track published: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 訂閱軌道
	err := publication.SetSubscribed(true)
	if err != nil {
		log.Printf("Failed to subscribe to track: %v", err)
	}
}

func (r *recorderImpl) HandleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track subscribed: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	recorder := newTrackRecorder(track, publication, rp.Identity())
	r.tracks.Store(publication.SID(), recorder)
	go recorder.Start()
}

func (r *recorderImpl) HandleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track unpublished: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 停止並移除軌道錄製器
	if recorder, ok := r.tracks.Load(publication.SID()); ok {
		recorder.(structs.TrackRecorder).Stop()
		r.tracks.Delete(publication.SID())
	}
}

func newTrackRecorder(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participantIdentity string) structs.TrackRecorder {
	return &trackRecorderImpl{
		track:               track,
		publication:         publication,
		participantIdentity: participantIdentity,
		stopChan:            make(chan struct{}),
	}
}

func (tr *trackRecorderImpl) Start() {
	log.Printf("Started recording track %s from participant %s", tr.publication.SID(), tr.participantIdentity)

	// 创建输出文件
	filename := fmt.Sprintf("%s_%s.raw", tr.participantIdentity, tr.publication.SID())
	file, err := os.Create(filename)
	if err != nil {
		log.Printf("Error creating output file: %v", err)
		return
	}
	defer file.Close()

	for {
		select {
		case <-tr.stopChan:
			log.Printf("Stopped recording track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
			return
		default:
			// 读取 RTP 包
			rtpPacket, _, err := tr.track.ReadRTP()
			if err != nil {
				log.Printf("Error reading RTP: %v", err)
				continue
			}

			// 将 RTP 包写入文件
			_, err = file.Write(rtpPacket.Payload)
			if err != nil {
				log.Printf("Error writing to file: %v", err)
				continue
			}

			log.Printf("Received RTP packet of size %d from track %s", len(rtpPacket.Payload), tr.publication.SID())
		}
	}
}

func (tr *trackRecorderImpl) Stop() {
	close(tr.stopChan)
}

// Start 函數用於啟動錄製器
func Start() error {
	config, err := NewRecorderConfig()
	if err != nil {
		return fmt.Errorf("failed to create recorder config: %v", err)
	}

	recorder := NewRecorder(*config)
	return recorder.Start()
}
