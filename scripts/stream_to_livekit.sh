#!/bin/bash

set -e

# 函数：显示使用说明
show_usage() {
    echo "Usage: $0 <method> [input_file]"
    echo "Methods:"
    echo "  1 - FFmpeg stream to RTMP"
    echo "  2 - Create LiveKit Ingress (RTMP or WHIP)"
    echo "Example for method 1: $0 1 /path/to/your/video.mp4"
    echo "Example for method 2: $0 2 <rtmp|whip>"
}

# 检查参数
if [ "$#" -lt 1 ]; then
    show_usage
    exit 1
fi

METHOD=$1
INPUT_FILE=$2

# 加载 .env 文件
if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
else
    echo ".env file not found"
    exit 1
fi

# 检查必要的环境变量
if [ -z "$LIVEKIT_URL" ] || [ -z "$LIVEKIT_API_KEY" ] || [ -z "$LIVEKIT_API_SECRET" ] || [ -z "$LIVEKIT_ROOM_NAME" ]; then
    echo "Missing required environment variables. Please check your .env file."
    exit 1
fi

# 方法1：使用FFmpeg推流到RTMP
method_1() {
    if [ -z "$RTMP_URL" ] || [ -z "$STREAM_KEY" ]; then
        echo "RTMP_URL and STREAM_KEY must be set in the .env file"
        exit 1
    fi
    
    echo "Streaming to RTMP using FFmpeg..."
    ffmpeg -re -i "$INPUT_FILE" \
        -c:v libx264 -preset veryfast -maxrate 3000k -bufsize 6000k \
        -pix_fmt yuv420p -g 50 -c:a aac -b:a 160k -ac 2 -ar 44100 \
        -f flv "rtmps://dasiotalbert-jcf2ze7z.rtmp.livekit.cloud/x/2CptNtyBXQr8"
}

# 方法2：创建LiveKit Ingress
method_2() {
    if [ "$INPUT_FILE" != "rtmp" ] && [ "$INPUT_FILE" != "whip" ]; then
        echo "Please specify 'rtmp' or 'whip' as the input type for method 2"
        exit 1
    fi

    INPUT_TYPE=0
    if [ "$INPUT_FILE" == "whip" ]; then
        INPUT_TYPE=1
    fi

    echo "Creating LiveKit Ingress for ${INPUT_FILE}..."

    # 创建临时JSON配置文件
    INGRESS_CONFIG=$(mktemp)
    cat > "$INGRESS_CONFIG" << EOL
{
    "input_type": $INPUT_TYPE,
    "name": "Ingress_$(date +%Y%m%d_%H%M%S)",
    "room_name": "$LIVEKIT_ROOM_NAME",
    "participant_identity": "ingress_$(uuidgen)",
    "participant_name": "Ingress Participant",
    "enable_transcoding": true
}
EOL

    # 创建Ingress
    lk ingress create "$INGRESS_CONFIG"

    # 清理临时文件
    rm "$INGRESS_CONFIG"
}

# 执行选择的方法
case $METHOD in
    1)
        if [ -z "$INPUT_FILE" ]; then
            echo "Input file is required for method 1"
            show_usage
            exit 1
        fi
        method_1
        ;;
    2)
        method_2
        ;;
    *)
        echo "Invalid method selected"
        show_usage
        exit 1
        ;;
esac

echo "Process completed."