# Start from the official Go image with version 1.21
FROM golang:1.21-alpine

# Set the working directory
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code
COPY . .

# Expose the port the app runs on
EXPOSE 8080

# The command will be specified in docker-compose.yml