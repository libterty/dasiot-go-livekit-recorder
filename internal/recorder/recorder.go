package recorder

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	livekit "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder/structs"
)

// Recorder 实现 structs.Recorder 接口
type Recorder struct {
	config structs.RecorderConfig
	room   *lksdk.Room
	tracks sync.Map
}

// TrackRecorder 实现 structs.TrackRecorder 接口
type TrackRecorder struct {
	track               *webrtc.TrackRemote
	publication         *lksdk.RemoteTrackPublication
	participantIdentity string
	stopChan            chan struct{}
	outputDir           string
	config              structs.RecorderConfig
}

// NewRecorderConfig 从环境变量创建一个新的 RecorderConfig
func NewRecorderConfig() (*structs.RecorderConfig, error) {
	// 加载 .env 文件（如果存在）
	_ = godotenv.Load()

	config := &structs.RecorderConfig{
		LiveKitURL: os.Getenv("LIVEKIT_URL"),
		APIKey:     os.Getenv("LIVEKIT_API_KEY"),
		APISecret:  os.Getenv("LIVEKIT_API_SECRET"),
		RoomName:   os.Getenv("LIVEKIT_ROOM_NAME"),
		OutputDir:  os.Getenv("OUTPUT_DIR"),
	}

	// 如果没有设置 OUTPUT_DIR，使用默认值
	if config.OutputDir == "" {
		config.OutputDir = "/tmp/video"
	}

	// 检查必要的配置是否已设置
	if config.LiveKitURL == "" || config.APIKey == "" || config.APISecret == "" || config.RoomName == "" {
		return nil, fmt.Errorf("missing required configuration")
	}

	return config, nil
}

// NewRecorder 创建一个新的录制器实例
func NewRecorder(config structs.RecorderConfig) structs.Recorder {
	return &Recorder{
		config: config,
	}
}

// Start 开始录制过程
func (r *Recorder) Start() error {
	log.Println("Starting the recorder...")

	// 确保输出目录存在
	if err := os.MkdirAll(r.config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

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

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down recorder...")
	r.room.Disconnect()

	// 给所有的 track recorder 一些时间来完成关闭操作
	time.Sleep(2 * time.Second)

	return nil
}

func (r *Recorder) HandleTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track published: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 订阅轨道
	err := publication.SetSubscribed(true)
	if err != nil {
		log.Printf("Failed to subscribe to track: %v", err)
	}
}

func (r *Recorder) HandleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track subscribed: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	recorder := newTrackRecorder(track, publication, rp.Identity(), r.config.OutputDir, r.config)
	r.tracks.Store(publication.SID(), recorder)
	go recorder.Start()
}

func (r *Recorder) HandleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track unpublished: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	// 停止并移除轨道录制器
	if recorder, ok := r.tracks.Load(publication.SID()); ok {
		recorder.(structs.TrackRecorder).Stop()
		r.tracks.Delete(publication.SID())
	}
}

func newTrackRecorder(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participantIdentity string, outputDir string, config structs.RecorderConfig) structs.TrackRecorder {
	return &TrackRecorder{
		track:               track,
		publication:         publication,
		participantIdentity: participantIdentity,
		stopChan:            make(chan struct{}),
		outputDir:           outputDir,
		config:              config,
	}
}

func (tr *TrackRecorder) Start() {
	log.Printf("Started recording track %s from participant %s", tr.publication.SID(), tr.participantIdentity)

	// 创建 Egress 客户端
	egressClient := lksdk.NewEgressClient(tr.config.LiveKitURL, tr.config.APIKey, tr.config.APISecret)

	// 设置输出文件名
	fileName := fmt.Sprintf("%s_%s_%s.mp4", tr.participantIdentity, tr.publication.SID(), time.Now().Format("20060102_150405"))
	outputPath := filepath.Join(tr.outputDir, fileName)

	// 创建 TrackEgressRequest
	req := &livekit.TrackEgressRequest{
		RoomName: tr.config.RoomName,
		TrackId:  tr.track.ID(),
		Output: &livekit.TrackEgressRequest_File{
			File: &livekit.DirectFileOutput{
				Filepath: outputPath,
			},
		},
	}

	// 启动 Egress
	res, err := egressClient.StartTrackEgress(context.Background(), req)
	if err != nil {
		log.Printf("Failed to start egress for track %s: %v", tr.publication.SID(), err)
		return
	}

	log.Printf("Egress started for track %s. EgressID: %s", tr.publication.SID(), res.EgressId)

	// 使用 defer 确保在函数退出时尝试停止 Egress
	defer func() {
		log.Printf("Attempting to stop egress for track %s", tr.publication.SID())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := egressClient.StopEgress(ctx, &livekit.StopEgressRequest{
			EgressId: res.EgressId,
		})
		if err != nil {
			log.Printf("Failed to stop egress for track %s: %v", tr.publication.SID(), err)
		} else {
			log.Printf("Successfully stopped egress for track %s", tr.publication.SID())
		}
	}()

	// 等待停止信号或错误
	select {
	case <-tr.stopChan:
		log.Printf("Received stop signal for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	case <-time.After(24 * time.Hour): // 设置一个最大录制时间，例如 24 小时
		log.Printf("Maximum recording time reached for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	}

	log.Printf("Recording ended for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
}

func (tr *TrackRecorder) Stop() {
	log.Printf("Stopping recording for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	close(tr.stopChan)
	// 给一些时间让 Start 方法完成最后的操作
	time.Sleep(time.Second)
}

// Start 函数用于启动录制器
func Start() error {
	config, err := NewRecorderConfig()
	if err != nil {
		return fmt.Errorf("failed to create recorder config: %v", err)
	}

	recorder := NewRecorder(*config)
	return recorder.Start()
}
