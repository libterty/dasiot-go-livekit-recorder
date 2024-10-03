package recorder

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
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
	"github.com/shirou/gopsutil/cpu"

	"github.com/dasiot-go-livekit-recorder/livekit-recorder/internal/recorder/structs"
)

type Recorder struct {
	config   structs.RecorderConfig
	room     *lksdk.Room
	tracks   sync.Map
	s3Client *s3.Client
}

type TrackRecorder struct {
	audioTrack          *webrtc.TrackRemote
	videoTrack          *webrtc.TrackRemote
	audioPublication    *lksdk.RemoteTrackPublication
	videoPublication    *lksdk.RemoteTrackPublication
	participantIdentity string
	stopChan            chan struct{}
	config              structs.RecorderConfig
	s3Client            *s3.Client
}

func NewRecorderConfig() (*structs.RecorderConfig, error) {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

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

	if config.LiveKitURL == "" || config.APIKey == "" || config.APISecret == "" || config.RoomName == "" ||
		config.S3Endpoint == "" || config.S3BucketName == "" || config.S3AccessKey == "" || config.S3AccessSecret == "" {
		return nil, fmt.Errorf("missing required configuration")
	}

	return config, nil
}

func NewRecorder(config structs.RecorderConfig) (*Recorder, error) {
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
		Region: config.S3Region,
	}

	s3Client := s3.NewFromConfig(s3Config, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	return &Recorder{
		config:   config,
		s3Client: s3Client,
	}, nil
}

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

	key := rp.Identity()
	value, loaded := r.tracks.Load(key)
	if loaded {
		recorder := value.(*TrackRecorder)
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			recorder.audioTrack = track
			recorder.audioPublication = publication
		} else if track.Kind() == webrtc.RTPCodecTypeVideo {
			recorder.videoTrack = track
			recorder.videoPublication = publication
		}
		if recorder.audioTrack != nil && recorder.videoTrack != nil {
			go recorder.Start()
		}
	} else {
		recorder := newTrackRecorder(rp.Identity(), r.config, r.s3Client)
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			recorder.audioTrack = track
			recorder.audioPublication = publication
		} else if track.Kind() == webrtc.RTPCodecTypeVideo {
			recorder.videoTrack = track
			recorder.videoPublication = publication
		}
		r.tracks.Store(key, recorder)
	}
}

func (r *Recorder) HandleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	log.Printf("Track unpublished: %s (%s) from participant %s", publication.SID(), publication.Kind(), rp.Identity())

	key := rp.Identity()
	if recorder, ok := r.tracks.Load(key); ok {
		recorder.(*TrackRecorder).Stop()
		r.tracks.Delete(key)
	}
}

func newTrackRecorder(participantIdentity string, config structs.RecorderConfig, s3Client *s3.Client) *TrackRecorder {
	return &TrackRecorder{
		participantIdentity: participantIdentity,
		stopChan:            make(chan struct{}),
		config:              config,
		s3Client:            s3Client,
	}
}

func (tr *TrackRecorder) Start() {
	if tr.audioTrack == nil || tr.videoTrack == nil {
		log.Printf("Cannot start recording for participant %s: missing audio or video track", tr.participantIdentity)
		return
	}

	log.Printf("Started recording tracks for participant %s", tr.participantIdentity)

	egressClient := lksdk.NewEgressClient(tr.config.LiveKitURL, tr.config.APIKey, tr.config.APISecret)

	fileName := fmt.Sprintf("ingress_%s_%s.mp4", tr.participantIdentity, time.Now().Format("20060102_150405"))
	s3Key := fmt.Sprintf("livecall/test/%s", fileName)

	expectedS3URL := fmt.Sprintf("https://%s/%s", tr.config.S3Endpoint, s3Key)
	log.Printf("Expected S3 URL: %s", expectedS3URL)

	req := &livekit.TrackCompositeEgressRequest{
		RoomName:     tr.config.RoomName,
		AudioTrackId: tr.audioTrack.ID(),
		VideoTrackId: tr.videoTrack.ID(),
		Output: &livekit.TrackCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				Filepath: s3Key,
				Output: &livekit.EncodedFileOutput_S3{
					S3: &livekit.S3Upload{
						AccessKey:      tr.config.S3AccessKey,
						Secret:         tr.config.S3AccessSecret,
						Bucket:         tr.config.S3BucketName,
						Endpoint:       tr.config.S3Endpoint,
						ForcePathStyle: true,
						Region:         tr.config.S3Region,
					},
				},
			},
		},
	}

	log.Printf("Starting egress for participant %s. Bucket: %s, Key: %s, Endpoint: %s",
		tr.participantIdentity, tr.config.S3BucketName, s3Key, tr.config.S3Endpoint)

	res, err := egressClient.StartTrackCompositeEgress(context.Background(), req)
	if err != nil {
		log.Printf("Failed to start egress for participant %s: %v", tr.participantIdentity, err)
		return
	}

	log.Printf("Egress started successfully for participant %s. EgressID: %s", tr.participantIdentity, res.EgressId)

	var lastKnownStatus livekit.EgressStatus

	// Start Egress status monitoring
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				listRes, err := egressClient.ListEgress(context.Background(), &livekit.ListEgressRequest{
					RoomName: tr.config.RoomName,
				})
				if err != nil {
					log.Printf("Error listing egress for participant %s: %v", tr.participantIdentity, err)
					continue
				}

				for _, info := range listRes.Items {
					if info.EgressId == res.EgressId {
						lastKnownStatus = info.Status
						log.Printf("Egress status for participant %s: %s", tr.participantIdentity, info.Status)

						// Log CPU and Memory usage
						tr.logResourceUsage()

						if info.Status == livekit.EgressStatus_EGRESS_COMPLETE {
							log.Printf("Egress completed successfully for participant %s", tr.participantIdentity)
							return
						} else if info.Status == livekit.EgressStatus_EGRESS_FAILED {
							log.Printf("Egress failed for participant %s. Error: %s", tr.participantIdentity, info.Error)
							if strings.Contains(info.Error, "AccessDenied") {
								log.Printf("S3 access denied. Please check your credentials and bucket permissions.")
							} else if strings.Contains(info.Error, "NoSuchBucket") {
								log.Printf("S3 bucket not found. Please check if the bucket '%s' exists.", tr.config.S3BucketName)
							}
							return
						} else if info.Status == livekit.EgressStatus_EGRESS_ABORTED {
							log.Printf("Egress aborted for participant %s", tr.participantIdentity)
							return
						}
						break
					}
				}

				// Check if we need to stop the egress based on lastKnownStatus
				if lastKnownStatus == livekit.EgressStatus_EGRESS_COMPLETE ||
					lastKnownStatus == livekit.EgressStatus_EGRESS_FAILED ||
					lastKnownStatus == livekit.EgressStatus_EGRESS_ABORTED {
					log.Printf("Stopping egress monitoring for participant %s due to terminal state: %s", tr.participantIdentity, lastKnownStatus)
					return
				}

			case <-tr.stopChan:
				log.Printf("Stop signal received for egress status monitoring of participant %s", tr.participantIdentity)
				// Attempt to stop the egress if it's still running
				if lastKnownStatus != livekit.EgressStatus_EGRESS_COMPLETE &&
					lastKnownStatus != livekit.EgressStatus_EGRESS_FAILED &&
					lastKnownStatus != livekit.EgressStatus_EGRESS_ABORTED {
					_, err := egressClient.StopEgress(context.Background(), &livekit.StopEgressRequest{
						EgressId: res.EgressId,
					})
					if err != nil {
						log.Printf("Failed to stop egress for participant %s: %v", tr.participantIdentity, err)
					} else {
						log.Printf("Successfully stopped egress for participant %s", tr.participantIdentity)
					}
				}
				return
			}
		}
	}()
}

func (tr *TrackRecorder) logResourceUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	cpuPercent, err := cpu.Percent(0, false)
	if err != nil {
		log.Printf("Error getting CPU usage: %v", err)
		return
	}

	log.Printf("Resource usage for participant %s: CPU: %.2f%%, Memory: Alloc = %v MiB, TotalAlloc = %v MiB, Sys = %v MiB, NumGC = %v",
		tr.participantIdentity,
		cpuPercent[0],
		bToMb(m.Alloc),
		bToMb(m.TotalAlloc),
		bToMb(m.Sys),
		m.NumGC)
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

func (tr *TrackRecorder) Stop() {
	log.Printf("Stopping recording for participant %s", tr.participantIdentity)
	close(tr.stopChan)
}

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
