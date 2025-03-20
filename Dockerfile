FROM golang:1.19-alpine AS build

WORKDIR /app

# Copy Go module files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY *.go ./
COPY query.logql ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o loki-log-exporter

# Create a smaller final image
FROM alpine:3.16

WORKDIR /app

# Copy the binary
COPY --from=build /app/loki-log-exporter .

# Create data directory for query file
RUN mkdir -p /data
COPY query.logql /data/query

# Create a non-root user and switch to it
RUN adduser -D -H -h /app appuser && \
    chown -R appuser:appuser /data

# Make the data directory writable for the appuser
USER appuser

# Run the application
CMD ["./loki-log-exporter"] 