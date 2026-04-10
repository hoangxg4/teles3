# S3Tele Specification

## Project Overview
- **Name**: S3Tele
- **Type**: S3-compatible storage server (binary)
- **Language**: Go
- **Backend**: Telegram Group Topics (MTProto)

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  S3 Client  │────▶│  S3Tele      │────▶│  Telegram       │
│ (AWS CLI,   │     │  (binary)    │     │  Group Topics   │
│  minio)     │     │              │     │  (Buckets)      │
└─────────────┘     └──────────────┘     └─────────────────┘
```

## Implementation

### Binary
- Go 1.25+
- Static compile: ~17MB
- Platform: Linux ARM64 (also x64)

### Telegram Client (gotd/td)
- MTProto protocol
- Forum topics for buckets
- File upload via Telegram API

### S3 API
- ListBuckets, ListObjects
- PutObject, GetObject, DeleteObject
- HeadObject, Multipart upload (basic)
- Basic Auth compatible with MinIO clients

## Configuration
```yaml
server:
  host: "0.0.0.0"
  port: 9000
  accessKey: "minioadmin"
  secretKey: "minioadmin"

telegram:
  appId: 0
  appHash: ""
  groupId: -1001234567890

storage:
  chunkSize: 1048576
  maxFileSize: 524288000
```