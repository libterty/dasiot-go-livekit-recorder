package recorder

import (
	"context"
	"encoding/json"
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
	config        structs.RecorderConfig
	rooms         map[string]*lksdk.Room
	tracks        sync.Map
	participants  sync.Map // map[string]string to store participantIdentity -> roomName
	s3Client      *s3.Client
	livekitClient *lksdk.RoomServiceClient
	mu            sync.RWMutex
}

type TrackRecorder struct {
	audioTrack          *webrtc.TrackRemote
	videoTrack          *webrtc.TrackRemote
	audioPublication    *lksdk.RemoteTrackPublication
	videoPublication    *lksdk.RemoteTrackPublication
	roomName            string
	participantIdentity string
	stopChan            chan struct{}
	config              structs.RecorderConfig
	s3Client            *s3.Client
	recorder            *Recorder
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

	livekitClient := lksdk.NewRoomServiceClient(config.LiveKitURL, config.APIKey, config.APISecret)

	return &Recorder{
		config:        config,
		rooms:         make(map[string]*lksdk.Room),
		s3Client:      s3Client,
		livekitClient: livekitClient,
	}, nil
}

func (r *Recorder) Start() error {
	log.Println("Starting the recorder...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rooms, err := r.livekitClient.ListRooms(ctx, &livekit.ListRoomsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list rooms: %v", err)
	}

	log.Printf("Found %d existing rooms", len(rooms.Rooms))

	connectedRooms := make(map[string]bool)

	for _, room := range rooms.Rooms {
		if err := r.connectToRoomIfNotConnected(room.Name, connectedRooms); err != nil {
			log.Printf("Error connecting to room %s: %v", room.Name, err)
		}
	}

	go r.monitorNewRooms(ctx, connectedRooms)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down recorder...")
	r.disconnectAllRooms()

	return nil
}

func (r *Recorder) connectToRoomIfNotConnected(roomName string, connectedRooms map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if connectedRooms[roomName] {
		log.Printf("Already connected to room: %s, skipping", roomName)
		return nil
	}

	log.Printf("Connecting to room: %s", roomName)

	recorderIdentity := fmt.Sprintf("recorder-%s", roomName)
	r.participants.Store(recorderIdentity, roomName)

	room, err := lksdk.ConnectToRoom(r.config.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              r.config.APIKey,
		APISecret:           r.config.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: recorderIdentity,
		ParticipantName:     fmt.Sprintf("Recorder Bot - %s", roomName),
	}, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackPublished:   r.HandleTrackPublished,
			OnTrackUnpublished: r.HandleTrackUnpublished,
			OnTrackSubscribed:  r.HandleTrackSubscribed,
		},
		OnParticipantConnected: func(participant *lksdk.RemoteParticipant) {
			r.participants.Store(participant.Identity(), roomName)
			log.Printf("Participant %s connected to room %s", participant.Identity(), roomName)
		},
		OnParticipantDisconnected: func(participant *lksdk.RemoteParticipant) {
			r.participants.Delete(participant.Identity())
			log.Printf("Participant %s disconnected from room %s", participant.Identity(), roomName)
		},
	})

	if err != nil {
		r.participants.Delete(recorderIdentity)
		return fmt.Errorf("failed to connect to room %s: %v", roomName, err)
	}

	// Store information about all participants currently in the room
	for _, participant := range room.GetParticipants() {
		r.participants.Store(participant.Identity(), roomName)
		log.Printf("Stored existing participant %s for room %s", participant.Identity(), roomName)
	}

	r.rooms[roomName] = room
	connectedRooms[roomName] = true

	// Update room metadata to indicate recorder is connected
	if err := r.updateRoomMetadata(room, true, livekit.EgressStatus_EGRESS_ACTIVE); err != nil {
		log.Printf("Failed to update room metadata for %s: %v", roomName, err)
	}

	log.Printf("Successfully connected to room: %s", roomName)
	r.logParticipants() // Log the current state of participants
	return nil
}

func (r *Recorder) monitorNewRooms(ctx context.Context, connectedRooms map[string]bool) {
	// 5 second scan if there's a new room
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rooms, err := r.livekitClient.ListRooms(ctx, &livekit.ListRoomsRequest{})
			if err != nil {
				log.Printf("Failed to list rooms: %v", err)
				continue
			}

			log.Printf("Found %d existing rooms", len(rooms.Rooms))

			for _, room := range rooms.Rooms {
				if err := r.connectToRoomIfNotConnected(room.Name, connectedRooms); err != nil {
					log.Printf("Error connecting to new room %s: %v", room.Name, err)
				}
			}
		}
	}
}

func (r *Recorder) disconnectAllRooms() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, room := range r.rooms {
		// Update room metadata to indicate recorder is disconnected
		if err := r.updateRoomMetadata(room, false, livekit.EgressStatus_EGRESS_ENDING); err != nil {
			log.Printf("Failed to update room metadata for %s: %v", room.Name(), err)
		}
		room.Disconnect()
	}
}

// Make sure the updateRoomMetadata method is correctly updating both connection and recording status
func (r *Recorder) updateRoomMetadata(room *lksdk.Room, isConnected bool, isRecording livekit.EgressStatus) error {
	ctx := context.Background()

	metadata := room.Metadata()

	if metadata == "" {
		metadata = "{}"
	}

	var metadataMap map[string]interface{}
	if err := json.Unmarshal([]byte(metadata), &metadataMap); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %v", err)
	}

	metadataMap["recorderConnected"] = isConnected
	metadataMap["recorderStatus"] = getEgressStatusKey(isRecording)

	updatedMetadata, err := json.Marshal(metadataMap)
	if err != nil {
		return fmt.Errorf("failed to marshal updated metadata: %v", err)
	}

	// Update room with new metadata
	_, err = r.livekitClient.UpdateRoomMetadata(ctx, &livekit.UpdateRoomMetadataRequest{
		Room:     room.Name(),
		Metadata: string(updatedMetadata),
	})
	if err != nil {
		return fmt.Errorf("failed to update room metadata: %v", err)
	}

	log.Printf("Updated metadata for room %s: recorderConnected = %v, isRecording = %v", room.Name(), isConnected, isRecording)
	return nil
}

func (r *Recorder) HandleTrackPublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	var roomName interface{}
	var ok bool
	for retries := 0; retries < 10; retries++ { // Increased retry count
		roomName, ok = r.participants.Load(rp.Identity())
		if ok {
			break
		}
		time.Sleep(200 * time.Millisecond) // Increased sleep time
	}
	if !ok {
		log.Printf("Warning: Room not found for participant %s in HandleTrackPublished after retries", rp.Identity())
		r.logParticipants() // Log the current state of participants
		return
	}

	log.Printf("Track published in room %s: %s (%s) from participant %s", roomName, publication.SID(), publication.Kind(), rp.Identity())

	if strings.HasPrefix(rp.Identity(), "recorder-") {
		log.Printf("Skipping subscription for recorder's own track")
		return
	}

	err := publication.SetSubscribed(true)
	if err != nil {
		log.Printf("Failed to subscribe to track in room %s: %v", roomName, err)
	}
}

func (r *Recorder) HandleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	var roomName interface{}
	var ok bool
	for retries := 0; retries < 10; retries++ { // Increased retry count
		roomName, ok = r.participants.Load(rp.Identity())
		if ok {
			break
		}
		time.Sleep(200 * time.Millisecond) // Increased sleep time
	}
	if !ok {
		log.Printf("Warning: Room not found for participant %s in HandleTrackSubscribed after retries", rp.Identity())
		r.logParticipants() // Log the current state of participants
		return
	}

	roomNameStr := roomName.(string)
	log.Printf("Track subscribed in room %s: %s (%s) from participant %s", roomNameStr, publication.SID(), publication.Kind(), rp.Identity())

	if strings.HasPrefix(rp.Identity(), "recorder-") {
		log.Printf("Skipping processing for recorder's own track")
		return
	}

	key := fmt.Sprintf("%s-%s", roomNameStr, rp.Identity())
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
		// Start recording if we have at least a video track
		if recorder.videoTrack != nil {
			go recorder.Start()
		}
	} else {
		recorder := newTrackRecorder(roomNameStr, rp.Identity(), r.config, r.s3Client, r)
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			recorder.audioTrack = track
			recorder.audioPublication = publication
		} else if track.Kind() == webrtc.RTPCodecTypeVideo {
			recorder.videoTrack = track
			recorder.videoPublication = publication
		}
		r.tracks.Store(key, recorder)
		// Start recording if we have at least a video track
		if recorder.videoTrack != nil {
			go recorder.Start()
		}
	}
}

func (r *Recorder) HandleTrackUnpublished(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	roomName, ok := r.participants.Load(rp.Identity())
	if !ok {
		log.Printf("Warning: Room not found for participant %s in HandleTrackUnpublished", rp.Identity())
		return
	}
	roomNameStr := roomName.(string)
	log.Printf("Track unpublished in room %s: %s (%s) from participant %s", roomNameStr, publication.SID(), publication.Kind(), rp.Identity())

	key := fmt.Sprintf("%s-%s", roomNameStr, rp.Identity())
	if recorder, ok := r.tracks.Load(key); ok {
		recorder.(*TrackRecorder).Stop() // This will update the isRecording status
		r.tracks.Delete(key)
	}
}

func (r *Recorder) logParticipants() {
	log.Println("Current participants:")
	r.participants.Range(func(key, value interface{}) bool {
		log.Printf("Participant: %v, Room: %v", key, value)
		return true
	})
}

func newTrackRecorder(roomName string, participantIdentity string, config structs.RecorderConfig, s3Client *s3.Client, recorder *Recorder) *TrackRecorder {
	return &TrackRecorder{
		roomName:            roomName,
		participantIdentity: participantIdentity,
		stopChan:            make(chan struct{}),
		config:              config,
		s3Client:            s3Client,
		recorder:            recorder,
	}
}

func (r *Recorder) updateRecordingStatus(roomName string, recordingStatus livekit.EgressStatus) error {
	r.mu.RLock()
	room, exists := r.rooms[roomName]
	r.mu.RUnlock()

	if !exists {
		return fmt.Errorf("room %s not found", roomName)
	}

	return r.updateRoomMetadata(room, true, recordingStatus)
}

func (tr *TrackRecorder) Start() {
	if tr.videoTrack == nil {
		log.Printf("Cannot start recording for participant %s in room %s: missing video track", tr.participantIdentity, tr.roomName)
		return
	}

	log.Printf("Started recording tracks for participant %s in room %s", tr.participantIdentity, tr.roomName)

	// Update room metadata to indicate recording has started
	if err := tr.recorder.updateRecordingStatus(tr.roomName, livekit.EgressStatus_EGRESS_STARTING); err != nil {
		log.Printf("Failed to update recording status for room %s: %v", tr.roomName, err)
	}

	egressClient := lksdk.NewEgressClient(tr.config.LiveKitURL, tr.config.APIKey, tr.config.APISecret)

	fileName := fmt.Sprintf("ingress_%s_%s_%s.mp4", tr.roomName, tr.participantIdentity, time.Now().Format("20060102_150405"))
	s3Key := fmt.Sprintf("livecall/test/%s", fileName)

	expectedS3URL := fmt.Sprintf("https://%s/%s", tr.config.S3Endpoint, s3Key)
	log.Printf("Expected S3 URL: %s", expectedS3URL)

	s3Upload := &livekit.S3Upload{
		AccessKey: tr.config.S3AccessKey,
		Secret:    tr.config.S3AccessSecret,
		Bucket:    tr.config.S3BucketName,
		Endpoint:  tr.config.S3Endpoint,
		Region:    tr.config.S3Region,
	}

	if tr.config.S3Region == "minio" {
		s3Upload.ForcePathStyle = true
	}

	req := &livekit.TrackCompositeEgressRequest{
		RoomName: tr.roomName,
		Output: &livekit.TrackCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				Filepath: s3Key,
				Output: &livekit.EncodedFileOutput_S3{
					S3: s3Upload,
				},
			},
		},
	}

	// Only include AudioTrackId if we have an audio track
	if tr.audioTrack != nil {
		req.AudioTrackId = tr.audioTrack.ID()
	}

	if tr.videoTrack != nil {
		req.VideoTrackId = tr.videoTrack.ID()
	}

	log.Printf("Starting egress for participant %s in room %s. Bucket: %s, Key: %s, Endpoint: %s",
		tr.participantIdentity, tr.roomName, tr.config.S3BucketName, s3Key, tr.config.S3Endpoint)

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

						// Update room metadata to indicate recording has started
						if err := tr.recorder.updateRecordingStatus(tr.roomName, info.Status); err != nil {
							log.Printf("Failed to update recording status for room %s: %v", tr.roomName, err)
						}

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
					lastKnownStatus == livekit.EgressStatus_EGRESS_ABORTED ||
					lastKnownStatus == livekit.EgressStatus_EGRESS_LIMIT_REACHED {

					// Update room metadata to indicate recording has started
					if err := tr.recorder.updateRecordingStatus(tr.roomName, lastKnownStatus); err != nil {
						log.Printf("Failed to update recording status for room %s: %v", tr.roomName, err)
					}

					log.Printf("Stopping egress monitoring for participant %s due to terminal state: %s", tr.participantIdentity, lastKnownStatus)
					return
				}

			case <-tr.stopChan:
				log.Printf("Stop signal received for egress status monitoring of participant %s", tr.participantIdentity)
				// Attempt to stop the egress if it's still running
				if lastKnownStatus != livekit.EgressStatus_EGRESS_COMPLETE &&
					lastKnownStatus != livekit.EgressStatus_EGRESS_FAILED &&
					lastKnownStatus != livekit.EgressStatus_EGRESS_ABORTED &&
					lastKnownStatus != livekit.EgressStatus_EGRESS_LIMIT_REACHED {
					_, err := egressClient.StopEgress(context.Background(), &livekit.StopEgressRequest{
						EgressId: res.EgressId,
					})
					// Update room metadata to indicate recording has started
					if err := tr.recorder.updateRecordingStatus(tr.roomName, lastKnownStatus); err != nil {
						log.Printf("Failed to update recording status for room %s: %v", tr.roomName, err)
					}
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
	log.Printf("Stopping recording for participant %s in room %s", tr.participantIdentity, tr.roomName)
	close(tr.stopChan)

	// Update room metadata to indicate recording has stopped
	if err := tr.recorder.updateRecordingStatus(tr.roomName, livekit.EgressStatus_EGRESS_ENDING); err != nil {
		log.Printf("Failed to update recording status for room %s: %v", tr.roomName, err)
	}
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

func getEgressStatusKey(status livekit.EgressStatus) string {
	switch status {
	case livekit.EgressStatus_EGRESS_STARTING:
		return "STARTING"
	case livekit.EgressStatus_EGRESS_ACTIVE:
		return "ACTIVE"
	case livekit.EgressStatus_EGRESS_ENDING:
		return "ENDING"
	case livekit.EgressStatus_EGRESS_COMPLETE:
		return "ACTIVE"
	case livekit.EgressStatus_EGRESS_FAILED:
		return "FAILED"
	case livekit.EgressStatus_EGRESS_ABORTED:
		return "ABORTED"
	case livekit.EgressStatus_EGRESS_LIMIT_REACHED:
		return "LIMIT_REACHED"
	default:
		return "UNKNOWN"
	}
}
