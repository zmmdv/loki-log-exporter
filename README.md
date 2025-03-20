# Loki Log Exporter

A simple Go application that polls a Loki instance for specific logs and sends alerts to Slack when matching logs are found.

## Features

- Polls Loki at configurable intervals (default: every 1 second)
- Uses LogQL queries from a file to filter logs
- Sends formatted log entries to Slack
- Prevents duplicate alerts
- Detailed logging of query results and alert status
- Configurable via environment variables

## Configuration

### Environment Variables

The application is configured using the following environment variables:

- `SLACK_TOKEN` (required): Your Slack API token
- `CHANNEL_ID` (required): The Slack channel ID where alerts will be sent
- `LOKI_URL` (optional, default: `http://localhost:3100`): URL of your Loki instance
- `QUERY_FILE` (optional, default: `/data/query`): Path to the file containing the LogQL query
- `POLL_SECONDS` (optional, default: `1`): Polling interval in seconds
- `VERBOSE_LOGGING` (optional, default: `true`): Enable detailed logging

### Query File

The application reads the LogQL query from a file, which by default is located at `/data/query`. The query should be formatted according to Loki's LogQL syntax.

A sample query is included in the repository as `query.logql`, which is copied to the container's `/data/query` path during the build process.

## Running locally

1. Make sure you have Go 1.19 or later installed
2. Build the application:
   ```
   go build -o loki-log-exporter
   ```
3. Create a query file:
   ```
   mkdir -p /path/to/data
   cp query.logql /path/to/data/query
   ```
4. Set the required environment variables:
   ```
   export SLACK_TOKEN=your-slack-token
   export CHANNEL_ID=your-slack-channel-id
   export QUERY_FILE=/path/to/data/query
   ```
5. Run the application:
   ```
   ./loki-log-exporter
   ```

## Running with Docker

### Using Docker Run

```
docker run -d --name loki-log-exporter \
  -e LOKI_URL=http://loki:3100 \
  -e SLACK_TOKEN=your-slack-token \
  -e CHANNEL_ID=your-slack-channel-id \
  -e VERBOSE_LOGGING=true \
  -v /path/to/custom/query.logql:/data/query \
  --network loki-network \
  your-registry/loki-log-exporter:latest
```

### Using Docker Compose

1. Set environment variables in a `.env` file:
   ```
   SLACK_TOKEN=your-slack-token
   CHANNEL_ID=your-slack-channel-id
   ```
2. Optionally, uncomment and update the volume mount in `docker-compose.yml` to use a custom query file
3. Run with Docker Compose:
   ```
   docker-compose up -d
   ```

## Logs and Monitoring

The application provides detailed logs about:
- The query being sent to Loki
- The logs found in each polling interval
- Which logs are being sent as alerts to Slack
- Success or failure of sending alerts

Example log output:
```
2023/04/15 12:00:00 Starting Loki to Slack exporter with polling interval of 1 seconds
2023/04/15 12:00:00 Using Loki URL: http://localhost:3100
2023/04/15 12:00:00 Using query file: /data/query
2023/04/15 12:00:00 Query: {job="ingress-nginx/ingress-nginx"} |= "Requests status:" | ...
2023/04/15 12:00:00 Slack channel: C12345ABCDE
2023/04/15 12:00:00 Verbose logging: true
2023/04/15 12:00:01 Querying Loki with URL: http://localhost:3100/loki/api/v1/query_range?...
2023/04/15 12:00:01 Found log entry: Timestamp: 20/Mar/2025:08:42:10 +0000
2023/04/15 12:00:01 Sending alert to Slack: Timestamp: 20/Mar/2025:08:42:10 +0000
2023/04/15 12:00:01 Alert successfully sent to Slack
2023/04/15 12:00:01 Found 1 log entries in this poll
```

## License

MIT 