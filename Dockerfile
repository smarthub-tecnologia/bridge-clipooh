# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install git for fetching dependencies
RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o wa-bridge ./cmd/server/main.go

# Final stage
FROM alpine:latest

WORKDIR /app

# Add ca-certificates for HTTPS and tzdata for timezones
RUN apk --no-cache add ca-certificates tzdata

# Copy the binary from builder
COPY --from=builder /app/wa-bridge .

# Copy config and migrations (if needed at runtime)
COPY --from=builder /app/config.yaml .
COPY --from=builder /app/migrations ./migrations

# Expose the standard port
EXPOSE 8080

# Command to run the executable
CMD ["./wa-bridge"]
