package recorder

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder/structs"
)

// NewRecorder 創建一個新的錄製器實例
func NewRecorder(config structs.RecorderConfig) *structs.Recorder {
	return &structs.Recorder{
		Config: config,
	}
}

// Start 開始錄製過程
func (r *structs.Recorder) Start() error {
	log.Println("Starting the recorder...")

	room, err := lksdk.ConnectToRoom(r.Config.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              r.Config.APIKey,
		APISecret:           r.Config.APISecret,
		RoomName:            r.Config.RoomName,
		ParticipantIdentity: "recorder",
		ParticipantName:     "Recorder Bot",
	}, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished:   r.handleTrackPublished,
			OnTrackUnpublished: r.handleTrackUnpublished,
			OnTrackSubscribed:  r.handleTrackSubscribed,
		},
	})

	if err != nil {
		return fmt.Errorf("failed to connect to room: %v", err)
	}

	r.Room = room
	log.Println("Connected to room:", r.Config.RoomName)

	// 等待中斷信號
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down recorder...")
	r.Room.Disconnect()

	return nil
}

func (r *structs.Recorder) handleTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track published: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 訂閱軌道
	err := publication.SetSubscribed(true)
	if err != nil {
		log.Printf("Failed to subscribe to track: %v", err)
	}
}

func (r *structs.Recorder) handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track subscribed: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	recorder := newTrackRecorder(track, publication, rp.Identity())
	r.Tracks.Store(publication.SID(), recorder)
	go recorder.start()
}

func (r *structs.Recorder) handleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track unpublished: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 停止並移除軌道錄製器
	if recorder, ok := r.Tracks.Load(publication.SID()); ok {
		recorder.(*structs.TrackRecorder).stop()
		r.Tracks.Delete(publication.SID())
	}
}

func newTrackRecorder(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participantIdentity string) *structs.TrackRecorder {
	return &structs.TrackRecorder{
		Track:               track,
		Publication:         publication,
		ParticipantIdentity: participantIdentity,
		StopChan:            make(chan struct{}),
	}
}

func (tr *structs.TrackRecorder) start() {
	log.Printf("Started recording track %s from participant %s", tr.Publication.SID(), tr.ParticipantIdentity)

	for {
		select {
		case <-tr.StopChan:
			log.Printf("Stopped recording track %s from participant %s", tr.Publication.SID(), tr.ParticipantIdentity)
			return
		default:
			// 這裡應該實現實際的數據接收和保存邏輯
			rtpPacket, _, err := tr.Track.ReadRTP()
			if err != nil {
				log.Printf("Error reading RTP: %v", err)
				continue
			}
			log.Printf("Received RTP packet of size %d from track %s", len(rtpPacket.Payload), tr.Publication.SID())
		}
	}
}

func (tr *structs.TrackRecorder) stop() {
	close(tr.StopChan)
}

// Start 函數用於啟動錄製器
func Start() error {
	config, err := structs.NewRecorderConfig()
	if err != nil {
		return fmt.Errorf("failed to create recorder config: %v", err)
	}

	recorder := NewRecorder(*config)
	return recorder.Start()
}
