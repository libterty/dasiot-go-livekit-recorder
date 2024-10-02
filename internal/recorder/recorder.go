package recorder

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/joho/godotenv"
	livekit "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/webrtc/v3"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder/structs"
)

// Recorder implements structs.Recorder interface
type Recorder struct {
	config   structs.RecorderConfig
	room     *lksdk.Room
	tracks   sync.Map
	s3Client *s3.Client
}

// TrackRecorder implements structs.TrackRecorder interface
type TrackRecorder struct {
	track               *webrtc.TrackRemote
	publication         *lksdk.RemoteTrackPublication
	participantIdentity string
	stopChan            chan struct{}
	config              structs.RecorderConfig
	s3Client            *s3.Client
}

// NewRecorderConfig creates a new RecorderConfig from environment variables
func NewRecorderConfig() (*structs.RecorderConfig, error) {
	_ = godotenv.Load()

	config := &structs.RecorderConfig{
		LiveKitURL:     os.Getenv("LIVEKIT_URL"),
		APIKey:         os.Getenv("LIVEKIT_API_KEY"),
		APISecret:      os.Getenv("LIVEKIT_API_SECRET"),
		RoomName:       os.Getenv("LIVEKIT_ROOM_NAME"),
		S3Endpoint:     os.Getenv("S3_ENDPOINT"),
		S3BucketName:   os.Getenv("S3_BUCKET_NAME"),
		S3AccessKey:    os.Getenv("S3_ACCESS_KEY"),
		S3AccessSecret: os.Getenv("S3_ACCESS_SECRET"),
		S3Region:       os.Getenv("S3_REGION"),
	}

	if config.LiveKitURL == "" || config.APIKey == "" || config.APISecret == "" || config.RoomName == "" || config.S3Endpoint == "" || config.S3BucketName == "" {
		return nil, fmt.Errorf("missing required configuration")
	}

	return config, nil
}

// NewRecorder creates a new recorder instance
func NewRecorder(config structs.RecorderConfig) (structs.Recorder, error) {
	// Create a custom S3 client using the provided endpoint
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL:           config.S3Endpoint,
			SigningRegion: config.S3Region,
		}, nil
	})

	s3Config := aws.Config{
		EndpointResolverWithOptions: customResolver,
		Credentials: credentials.NewStaticCredentialsProvider(
			config.S3AccessKey,
			config.S3AccessSecret,
			"",
		),
		Region:           config.S3Region,
		RetryMaxAttempts: 3,
		RetryMode:        aws.RetryModeStandard,
	}

	// 禁用 S3 特定的默认行为
	options := []func(*s3.Options){
		func(o *s3.Options) {
			o.UsePathStyle = true // 对 MinIO 很重要
		},
	}

	s3Client := s3.NewFromConfig(s3Config, options...)

	return &Recorder{
		config:   config,
		s3Client: s3Client,
	}, nil
}

func checkEgressStatus(egressClient *lksdk.EgressClient, egressId string, roomName string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			listRes, err := egressClient.ListEgress(context.Background(), &livekit.ListEgressRequest{
				RoomName: roomName,
			})
			if err != nil {
				log.Printf("Error listing egress: %v", err)
				continue
			}

			for _, info := range listRes.Items {
				if info.EgressId == egressId {
					log.Printf("Egress status: %s", info.Status)
					if info.Status == livekit.EgressStatus_EGRESS_FAILED {
						log.Printf("Egress failed. Error: %s", info.Error)
						return
					} else if info.Status == livekit.EgressStatus_EGRESS_COMPLETE ||
						info.Status == livekit.EgressStatus_EGRESS_ABORTED {
						log.Printf("Egress completed with status: %s", info.Status)
						return
					}
					break
				}
			}
		}
	}
}

// Start begins the recording process
func (r *Recorder) Start() error {
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down recorder...")
	r.room.Disconnect()

	time.Sleep(2 * time.Second)

	return nil
}

func (r *Recorder) HandleTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track published: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	err := publication.SetSubscribed(true)
	if err != nil {
		log.Printf("Failed to subscribe to track: %v", err)
	}
}

func (r *Recorder) HandleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track subscribed: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	recorder := newTrackRecorder(track, publication, rp.Identity(), r.config, r.s3Client)
	r.tracks.Store(publication.SID(), recorder)
	go recorder.Start()
}

func (r *Recorder) HandleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track unpublished: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	if recorder, ok := r.tracks.Load(publication.SID()); ok {
		recorder.(structs.TrackRecorder).Stop()
		r.tracks.Delete(publication.SID())
	}
}

func newTrackRecorder(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participantIdentity string, config structs.RecorderConfig, s3Client *s3.Client) structs.TrackRecorder {
	return &TrackRecorder{
		track:               track,
		publication:         publication,
		participantIdentity: participantIdentity,
		stopChan:            make(chan struct{}),
		config:              config,
		s3Client:            s3Client,
	}
}

func (tr *TrackRecorder) Start() {
	log.Printf("Started recording track %s from participant %s", tr.publication.SID(), tr.participantIdentity)

	egressClient := lksdk.NewEgressClient(tr.config.LiveKitURL, tr.config.APIKey, tr.config.APISecret)

	fileName := fmt.Sprintf("%s_%s_%s.mp4", tr.participantIdentity, tr.publication.SID(), time.Now().Format("20060102_150405"))
	s3Key := fmt.Sprintf("recordings/%s", fileName)

	req := &livekit.TrackEgressRequest{
		RoomName: tr.config.RoomName,
		TrackId:  tr.track.ID(),
		Output: &livekit.TrackEgressRequest_File{
			File: &livekit.DirectFileOutput{
				Filepath: s3Key,
				Output: &livekit.DirectFileOutput_S3{
					S3: &livekit.S3Upload{
						AccessKey: tr.config.S3AccessKey,
						Secret:    tr.config.S3AccessSecret,
						Bucket:    tr.config.S3BucketName,
						Endpoint:  tr.config.S3Endpoint,
						Region:    tr.config.S3Region,
					},
				},
			},
		},
	}

	log.Printf("Starting egress for track %s with S3 Key: %s", tr.publication.SID(), s3Key)
	res, err := egressClient.StartTrackEgress(context.Background(), req)
	if err != nil {
		log.Printf("Failed to start egress for track %s: %v", tr.publication.SID(), err)
		return
	}

	log.Printf("Egress started successfully for track %s. EgressID: %s", tr.publication.SID(), res.EgressId)

	// 启动 Egress 状态监控
	egressDone := make(chan struct{})
	go func() {
		defer close(egressDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				listRes, err := egressClient.ListEgress(context.Background(), &livekit.ListEgressRequest{
					RoomName: tr.config.RoomName,
				})
				if err != nil {
					log.Printf("Error listing egress for track %s: %v", tr.publication.SID(), err)
					continue
				}

				for _, info := range listRes.Items {
					if info.EgressId == res.EgressId {
						log.Printf("Egress status for track %s: %s", tr.publication.SID(), info.Status)
						if info.Status == livekit.EgressStatus_EGRESS_COMPLETE {
							log.Printf("Egress completed successfully for track %s", tr.publication.SID())
							return
						} else if info.Status == livekit.EgressStatus_EGRESS_FAILED {
							log.Printf("Egress failed for track %s. Error: %s", tr.publication.SID(), info.Error)
							return
						} else if info.Status == livekit.EgressStatus_EGRESS_ABORTED {
							log.Printf("Egress aborted for track %s", tr.publication.SID())
							return
						}
						break
					}
				}
			case <-tr.stopChan:
				log.Printf("Stop signal received for egress status monitoring of track %s", tr.publication.SID())
				return
			}
		}
	}()

	// 监控 RTP 包
	go func() {
		packetCount := 0
		for {
			select {
			case <-tr.stopChan:
				log.Printf("Stop signal received for RTP monitoring of track %s", tr.publication.SID())
				return
			default:
				_, _, err := tr.track.ReadRTP()
				if err != nil {
					if err == io.EOF {
						log.Printf("End of stream reached for track %s", tr.publication.SID())
						return
					}
					log.Printf("Error reading RTP for track %s: %v", tr.publication.SID(), err)
					continue
				}
				packetCount++
				if packetCount%1000 == 0 {
					log.Printf("Received %d RTP packets for track %s", packetCount, tr.publication.SID())
				}
			}
		}
	}()

	// 等待录制结束
	select {
	case <-tr.stopChan:
		log.Printf("Received stop signal for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	case <-egressDone:
		log.Printf("Egress process completed for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	case <-time.After(24 * time.Hour):
		log.Printf("Maximum recording time reached for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	}

	// 停止 Egress
	log.Printf("Attempting to stop egress for track %s", tr.publication.SID())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = egressClient.StopEgress(ctx, &livekit.StopEgressRequest{
		EgressId: res.EgressId,
	})
	if err != nil {
		log.Printf("Failed to stop egress for track %s: %v", tr.publication.SID(), err)
	} else {
		log.Printf("Successfully stopped egress for track %s", tr.publication.SID())
	}

	log.Printf("Recording ended for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
}

func (tr *TrackRecorder) Stop() {
	log.Printf("Stopping recording for track %s from participant %s", tr.publication.SID(), tr.participantIdentity)
	close(tr.stopChan)
	time.Sleep(time.Second)
}

// Start function to start the recorder
func Start() error {
	config, err := NewRecorderConfig()
	if err != nil {
		return fmt.Errorf("failed to create recorder config: %v", err)
	}

	recorder, err := NewRecorder(*config)
	if err != nil {
		return fmt.Errorf("failed to create recorder: %v", err)
	}

	return recorder.Start()
}
