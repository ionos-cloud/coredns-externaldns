# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code and cmd directory
COPY *.go ./
COPY cmd/ ./cmd/

# Build the standalone CoreDNS with externaldns plugin
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o coredns-externaldns ./cmd/coredns-externaldns

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the standalone binary from builder stage
COPY --from=builder /workspace/coredns-externaldns .

# Expose DNS ports (UDP and TCP)
EXPOSE 53 53/udp

# Run the standalone CoreDNS
ENTRYPOINT [ "./coredns-externaldns" ]
CMD ["-conf", "Corefile"]
