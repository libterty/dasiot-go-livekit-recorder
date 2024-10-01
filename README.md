# LiveKit Recorder

This project is a LiveKit session recorder implemented in Go.

## Project Structure

This project follows the standard Go project layout as defined in [golang-standards/project-layout](https://github.com/golang-standards/project-layout).

## Setup

1. Ensure you have Go installed (version 1.17 or later).
2. Clone this repository.
3. Run `go mod tidy` to download dependencies.

## Usage

Run the project with:

```
make run
```

More instructions will be added as the project develops.

## Video Test

```
// Create a Livekit ingress first

./scripts/stream_to_livekit.sh 2



// After ingress created, copy create result `rtmp_url` and `stream_key` to env file

./scripts/stream_to_livekit.sh 1 <path-to-your-video-source>
```
