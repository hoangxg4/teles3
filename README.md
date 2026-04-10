# S3Tele

S3-compatible storage server sử dụng Telegram làm backend storage với Bot quản lý.

## 🚀 Quick Start

### Docker (Khuyến nghị)

```bash
docker run -d \
  -p 9000:9000 \
  -e BOT_TOKEN="your_bot_token" \
  ghcr.io/username/s3tele:latest
```

### Binary

```bash
# Download từ Releases
./s3tele
```

## ⚙️ Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_HOST` | 0.0.0.0 | Server host |
| `SERVER_PORT` | 9000 | Server port |
| `ACCESS_KEY` | minioadmin | Default access key |
| `SECRET_KEY` | minioadmin | Default secret key |
| `BOT_TOKEN` | - | Telegram Bot token |
| `BOT_ADMINS` | - | Admin user IDs (comma separated) |
| `DATA_DIR` | ./data | Data directory |

## 🤖 Bot Commands

Sau khi tạo bot và lấy token:

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/genkey` | Tạo access key mới |
| `/keys` | Xem keys của bạn |
| `/buckets` | List buckets |
| `/createbucket <name>` | Tạo bucket mới |
| `/deletebucket <name>` | Xóa bucket |

## 📡 S3 API

Sau khi có key từ bot, sử dụng với AWS CLI:

```bash
export AWS_ACCESS_KEY_ID=s3_12345678_xxxxx
export AWS_SECRET_ACCESS_KEY=your_secret_key

# List buckets
aws --endpoint-url=http://localhost:9000 s3 ls

# Create bucket
aws --endpoint-url=http://localhost:9000 s3 mb s3://mybucket

# Upload file
aws --endpoint-url=http://localhost:9000 s3 cp test.txt s3://mybucket/

# Download file
aws --endpoint-url=http://localhost:9000 s3 cp s3://mybucket/test.txt ./
```

## 🐳 Docker Build locally

```bash
# Build binary first
export GOROOT=/tmp/go-arm
go build -ldflags="-s -w" -o s3tele ./cmd/s3tele

# Build Docker
docker build -t s3tele:latest .
```

## 📦 Releases

- **Binary**: `s3tele-linux-amd64`, `s3tele-linux-arm64`
- **Docker**: `ghcr.io/username/s3tele:latest` (multi-arch)

## 🔧 Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  S3 Client  │────▶│  S3Tele      │────▶│  Telegram       │
│ (AWS CLI)   │     │  (Bot + API) │     │  (Storage)      │
└─────────────┘     └──────────────┘     └─────────────────┘
```

## 📝 License

ISC