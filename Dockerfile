# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build static binary
# Note: VERSION is passed as build arg during release builds
# Example: docker build --build-arg VERSION=v0.3.0
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION}" -a -installsuffix cgo -o pentameter .

# Final stage
FROM scratch

# Copy the binary from builder stage
COPY --from=builder /app/pentameter /pentameter

# Copy CA certificates for HTTPS (if needed)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 8080

ENTRYPOINT ["/pentameter"]