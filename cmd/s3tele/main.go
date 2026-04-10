package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Telegram TelegramConfig `yaml:"telegram"`
	Storage  StorageConfig  `yaml:"storage"`
	Bot      BotConfig      `yaml:"bot"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	AccessKey string `yaml:"accessKey"`
	SecretKey string `yaml:"secretKey"`
}

type TelegramConfig struct {
	AppID   int    `yaml:"appId"`
	AppHash string `yaml:"appHash"`
	GroupID int64  `yaml:"groupId"`
}

type StorageConfig struct {
	ChunkSize   int64 `yaml:"chunkSize"`
	MaxFileSize int64 `yaml:"maxFileSize"`
}

type BotConfig struct {
	Token   string   `yaml:"token"`
	Admins  []int64  `yaml:"admins"`
}

type UserCredentials struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	ChatID    int64  `json:"chatId"`
	CreatedAt int64  `json:"createdAt"`
}

type UserBucket struct {
	Name      string `json:"name"`
	TopicID   int64  `json:"topicId"`
	ChatID    int64  `json:"chatId"`
	CreatedAt int64  `json:"createdAt"`
}

type BotState struct {
	credentials map[int64]*UserCredentials
	buckets     map[int64]map[string]*UserBucket
	mu          sync.RWMutex
	storagePath string
}

func NewBotState(path string) *BotState {
	state := &BotState{
		credentials: make(map[int64]*UserCredentials),
		buckets:     make(map[int64]map[string]*UserBucket),
		storagePath: path,
	}
	state.load()
	return state
}

func (s *BotState) load() {
	if data, err := os.ReadFile(s.storagePath); err == nil {
		var saved struct {
			Credentials map[int64]*UserCredentials `json:"credentials"`
			Buckets     map[int64]map[string]*UserBucket `json:"buckets"`
		}
		if json.Unmarshal(data, &saved) == nil {
			s.credentials = saved.Credentials
			s.buckets = saved.Buckets
		}
	}
}

func (s *BotState) save() {
	data, _ := json.MarshalIndent(map[string]interface{}{
		"credentials": s.credentials,
		"buckets":     s.buckets,
	}, "", "  ")
	os.WriteFile(s.storagePath, data, 0644)
}

func (s *BotState) GetCredentials(chatID int64) *UserCredentials {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.credentials[chatID]
}

func (s *BotState) SetCredentials(chatID int64, cred *UserCredentials) {
	s.mu.Lock()
	s.credentials[chatID] = cred
	s.mu.Unlock()
	s.save()
}

func (s *BotState) GetBuckets(chatID int64) map[string]*UserBucket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.buckets[chatID]; !ok {
		s.buckets[chatID] = make(map[string]*UserBucket)
	}
	return s.buckets[chatID]
}

func (s *BotState) SetBucket(chatID int64, bucket *UserBucket) {
	s.mu.Lock()
	if _, ok := s.buckets[chatID]; !ok {
		s.buckets[chatID] = make(map[string]*UserBucket)
	}
	s.buckets[chatID][bucket.Name] = bucket
	s.mu.Unlock()
	s.save()
}

func (s *BotState) DeleteBucket(chatID int64, name string) {
	s.mu.Lock()
	if buckets, ok := s.buckets[chatID]; ok {
		delete(buckets, name)
	}
	s.mu.Unlock()
	s.save()
}

type Storage struct {
	tg     *telegram.Client
	config StorageConfig
}

func NewStorage(tg *telegram.Client, config StorageConfig) *Storage {
	return &Storage{tg: tg, config: config}
}

func (s *Storage) UploadObject(ctx context.Context, chatID int64, bucket, object string, data []byte) (string, int64, error) {
	hash := md5.Sum(data)
	return "\"" + hex.EncodeToString(hash[:]) + "\"", int64(len(data)), nil
}

func (s *Storage) GetObject(ctx context.Context, chatID int64, bucket, object string) ([]byte, error) {
	return nil, nil
}

func (s *Storage) DeleteObject(ctx context.Context, chatID int64, bucket, object string) error {
	return nil
}

type S3Server struct {
	storage    *Storage
	config     ServerConfig
	botState   *BotState
	botToken   string
	admins     map[int64]bool
	mux        *http.ServeMux
}

func NewS3Server(storage *Storage, config ServerConfig, botState *BotState, botToken string, admins []int64) *S3Server {
	adminsMap := make(map[int64]bool)
	for _, a := range admins {
		adminsMap[a] = true
	}
	s := &S3Server{
		storage:  storage,
		config:   config,
		botState: botState,
		botToken: botToken,
		admins:   adminsMap,
		mux:      http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *S3Server) setupRoutes() {
	s.mux.HandleFunc("/", s.handle)
}

func (s *S3Server) handle(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		s.sendError(w, http.StatusUnauthorized, "No authorization header")
		return
	}

	var accessKey, secretKey string
	if strings.HasPrefix(authHeader, "Basic ") {
		encoded := strings.TrimPrefix(authHeader, "Basic ")
		decoded, err := hex.DecodeString(strings.ReplaceAll(encoded, "=", ""))
		if err == nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				accessKey, secretKey = parts[0], parts[1]
			}
		}
	}

	if accessKey == "" || secretKey == "" {
		s.sendError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	var userCred *UserCredentials
	for _, cred := range s.botState.credentials {
		if cred.AccessKey == accessKey && cred.SecretKey == secretKey {
			userCred = cred
			break
		}
	}

	if userCred == nil {
		s.sendError(w, http.StatusForbidden, "Invalid credentials")
		return
	}

	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := ""
	object := ""
	if len(parts) >= 1 {
		bucket = parts[0]
	}
	if len(parts) >= 2 {
		object = parts[1]
	}

	switch r.Method {
	case "GET":
		if bucket == "" {
			s.listBuckets(w, r, userCred)
		} else if object == "" {
			s.listObjects(w, r, userCred, bucket)
		} else {
			s.getObject(w, r, userCred, bucket, object)
		}
	case "PUT":
		if object == "" {
			s.createBucket(w, r, userCred, bucket)
		} else {
			s.putObject(w, r, userCred, bucket, object)
		}
	case "DELETE":
		if object == "" {
			s.deleteBucket(w, r, userCred, bucket)
		} else {
			s.deleteObject(w, r, userCred, bucket, object)
		}
	case "HEAD":
		s.headObject(w, r, userCred, bucket, object)
	default:
		w.Header().Set("Allow", "GET, PUT, HEAD, DELETE")
		s.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *S3Server) listBuckets(w http.ResponseWriter, r *http.Request, cred *UserCredentials) {
	buckets := s.botState.GetBuckets(cred.ChatID)
	contents := make([]BucketInfo, 0, len(buckets))
	for name, b := range buckets {
		contents = append(contents, BucketInfo{Name: name, Created: time.UnixMilli(b.CreatedAt)})
	}
	s.sendXML(w, ListBucketsResp{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/", Buckets: BucketList{Items: contents}, Owner: Owner{ID: cred.AccessKey, DisplayName: "user"}})
}

func (s *S3Server) listObjects(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket string) {
	buckets := s.botState.GetBuckets(cred.ChatID)
	if _, ok := buckets[bucket]; !ok {
		s.sendError(w, http.StatusNotFound, "Bucket not found")
		return
	}
	s.sendXML(w, ListObjectsResp{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/", Name: bucket})
}

func (s *S3Server) createBucket(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket string) {
	if strings.Contains(bucket, "..") || strings.Contains(bucket, "/") {
		s.sendError(w, http.StatusBadRequest, "Invalid bucket name")
		return
	}

	buckets := s.botState.GetBuckets(cred.ChatID)
	if _, exists := buckets[bucket]; exists {
		s.sendError(w, http.StatusConflict, "Bucket already exists")
		return
	}

	s.botState.SetBucket(cred.ChatID, &UserBucket{
		Name:      bucket,
		ChatID:    cred.ChatID,
		CreatedAt: time.Now().UnixMilli(),
	})
	w.WriteHeader(http.StatusOK)
}

func (s *S3Server) putObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, object string) {
	data, _ := io.ReadAll(r.Body)
	hash := md5.Sum(data)
	etag := "\"" + hex.EncodeToString(hash[:]) + "\""

	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (s *S3Server) getObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, object string) {
	s.sendError(w, http.StatusNotFound, "Object not found")
}

func (s *S3Server) deleteBucket(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket string) {
	s.botState.DeleteBucket(cred.ChatID, bucket)
	w.WriteHeader(http.StatusNoContent)
}

func (s *S3Server) deleteObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, object string) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *S3Server) headObject(w http.ResponseWriter, r *http.Request, cred *UserCredentials, bucket, object string) {
	s.sendError(w, http.StatusNotFound, "Object not found")
}

func (s *S3Server) sendError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/xml")
	resp := ErrorResp{Code: http.StatusText(status), Message: message, RequestID: fmt.Sprintf("%x", md5.Sum([]byte(time.Now().String())))}
	xml.NewEncoder(w).Encode(resp)
}

func (s *S3Server) sendXML(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(v)
}

func (s *S3Server) Start(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

type ListBucketsResp struct {
	XMLName xml.Name   `xml:"ListAllMyBucketsResult"`
	XMLNS   string     `xml:"xmlns,attr"`
	Buckets BucketList `xml:"Buckets"`
	Owner   Owner      `xml:"Owner"`
}

type BucketList struct {
	Items []BucketInfo `xml:"Bucket"`
}

type BucketInfo struct {
	Name    string    `xml:"Name"`
	Created time.Time `xml:"CreationDate"`
}

type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type ListObjectsResp struct {
	XMLName xml.Name `xml:"ListBucketResult"`
	XMLNS   string   `xml:"xmlns,attr"`
	Name    string   `xml:"Name"`
}

type ErrorResp struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
}

type BotUpdate struct {
	UpdateID int64        `json:"update_id"`
	Message  BotMessage  `json:"message"`
}

type BotMessage struct {
	MessageID int64  `json:"message_id"`
	From      BotUser `json:"from"`
	Chat      BotChat `json:"chat"`
	Text      string `json:"text"`
}

type BotUser struct {
	ID int64 `json:"id"`
}

type BotChat struct {
	ID int64 `json:"id"`
}

func (s *S3Server) StartBot(ctx context.Context) {
	if s.botToken == "" {
		return
	}

	offset := int64(0)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	fmt.Println("🤖 Bot polling started...")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=60&offset=%d", s.botToken, offset)
			resp, err := http.Get(url)
			if err != nil {
				continue
			}

			var result struct {
				OK     bool        `json:"ok"`
				Result []BotUpdate `json:"result"`
			}
			if json.NewDecoder(resp.Body).Decode(&result) == nil && result.OK {
				for _, upd := range result.Result {
					offset = upd.UpdateID + 1
					s.handleBotMessage(upd.Message)
				}
			}
			resp.Body.Close()
		}
	}
}

func (s *S3Server) handleBotMessage(msg BotMessage) {
	chatID := msg.Chat.ID
	text := msg.Text

	if text == "" || !strings.HasPrefix(text, "/") {
		return
	}

	parts := strings.Fields(text)
	cmd := parts[0]

	var response string
	switch cmd {
	case "/start":
		response = "Welcome to S3Tele Bot!\n\nCommands:\n/keys - Show your access keys\n/genkey - Generate new keys\n/buckets - List your buckets\n/help - Show help"
	case "/help":
		response = "Commands:\n/keys - Show access keys\n/genkey - Generate new keys\n/buckets - List buckets\n/createbucket <name> - Create bucket\n/deletebucket <name> - Delete bucket"
	case "/keys":
		if cred := s.botState.GetCredentials(chatID); cred != nil {
			response = fmt.Sprintf("Access Key: `%s`\nSecret Key: `%s`", cred.AccessKey, cred.SecretKey)
		} else {
			response = "No keys found. Use /genkey to generate."
		}
	case "/genkey":
		accessKey := fmt.Sprintf("s3_%d_%s", chatID, randomString(8))
		secretKey := randomString(32)
		s.botState.SetCredentials(chatID, &UserCredentials{
			AccessKey: accessKey,
			SecretKey: secretKey,
			ChatID:    chatID,
			CreatedAt: time.Now().UnixMilli(),
		})
		response = fmt.Sprintf("✅ Keys generated!\n\nAccess Key: `%s`\nSecret Key: `%s`", accessKey, secretKey)
	case "/buckets":
		buckets := s.botState.GetBuckets(chatID)
		if len(buckets) == 0 {
			response = "No buckets found."
		} else {
			var names []string
			for name := range buckets {
				names = append(names, name)
			}
			response = "Your buckets:\n" + strings.Join(names, "\n")
		}
	case "/createbucket":
		if len(parts) < 2 {
			response = "Usage: /createbucket <name>"
		} else {
			name := parts[1]
			s.botState.SetBucket(chatID, &UserBucket{
				Name:      name,
				ChatID:    chatID,
				CreatedAt: time.Now().UnixMilli(),
			})
			response = fmt.Sprintf("✅ Bucket `%s` created!", name)
		}
	case "/deletebucket":
		if len(parts) < 2 {
			response = "Usage: /deletebucket <name>"
		} else {
			name := parts[1]
			s.botState.DeleteBucket(chatID, name)
			response = fmt.Sprintf("✅ Bucket `%s` deleted!", name)
		}
	}

	if response != "" {
		go s.sendMessage(chatID, response)
	}
}

func (s *S3Server) sendMessage(chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.botToken)
	http.Post(url, "application/json", strings.NewReader(fmt.Sprintf(`{"chat_id":%d,"text":"%s"}`, chatID, text)))
}

func randomString(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	r := time.Now().UnixNano()
	for i := range b {
		b[i] = chars[int(r+int64(i)*31)%len(chars)]
	}
	return string(b)
}

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		val, _ := strconv.Atoi(v)
		return val
	}
	return defaultValue
}

func getEnvInt64OrDefault(key string, defaultValue int64) int64 {
	if v := os.Getenv(key); v != "" {
		val, _ := strconv.ParseInt(v, 10, 64)
		return val
	}
	return defaultValue
}

func getEnvIntSlice(key string) []int64 {
	if v := os.Getenv(key); v != "" {
		var result []int64
		for _, s := range strings.Split(v, ",") {
			if val, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				result = append(result, val)
			}
		}
		return result
	}
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return &config, nil
}

func LoadConfigFromEnv() *Config {
	return &Config{
		Server: ServerConfig{
			Host:      getEnvOrDefault("SERVER_HOST", "0.0.0.0"),
			Port:      getEnvIntOrDefault("SERVER_PORT", 9000),
			AccessKey: getEnvOrDefault("ACCESS_KEY", "minioadmin"),
			SecretKey: getEnvOrDefault("SECRET_KEY", "minioadmin"),
		},
		Telegram: TelegramConfig{
			AppID:   getEnvIntOrDefault("TELEGRAM_APP_ID", 0),
			AppHash: getEnvOrDefault("TELEGRAM_APP_HASH", ""),
			GroupID: getEnvInt64OrDefault("TELEGRAM_GROUP_ID", 0),
		},
		Storage: StorageConfig{
			ChunkSize:   1048576,
			MaxFileSize: 524288000,
		},
		Bot: BotConfig{
			Token:  getEnvOrDefault("BOT_TOKEN", ""),
			Admins: getEnvIntSlice("BOT_ADMINS"),
		},
	}
}

func main() {
	configPath := flag.String("config", "", "Path to config file")
	flag.Parse()

	var config *Config
	var err error

	if *configPath != "" {
		config, err = LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
	} else {
		config = LoadConfigFromEnv()
	}

	if v := os.Getenv("SERVER_HOST"); v != "" {
		config.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		config.Server.Port, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("ACCESS_KEY"); v != "" {
		config.Server.AccessKey = v
	}
	if v := os.Getenv("SECRET_KEY"); v != "" {
		config.Server.SecretKey = v
	}
	if v := os.Getenv("BOT_TOKEN"); v != "" {
		config.Bot.Token = v
	}
	if v := os.Getenv("BOT_ADMINS"); v != "" {
		config.Bot.Admins = getEnvIntSlice("BOT_ADMINS")
	}

	fmt.Println("🔄 Initializing S3Tele...")

	statePath := os.Getenv("DATA_DIR")
	if statePath == "" {
		statePath = "./data"
	}
	os.MkdirAll(statePath, 0755)
	botState := NewBotState(filepath.Join(statePath, "bot_state.json"))

	var tgClient *telegram.Client
	if config.Telegram.AppID > 0 && config.Telegram.AppHash != "" {
		tgClient = telegram.NewClient(config.Telegram.AppID, config.Telegram.AppHash, telegram.Options{})
		go func() {
			if err := tgClient.Run(context.Background(), func(ctx context.Context) error {
				return nil
			}); err != nil {
				log.Printf("Telegram client error: %v", err)
			}
		}()
	}

	storageConfig := StorageConfig{
		ChunkSize:   config.Storage.ChunkSize,
		MaxFileSize: config.Storage.MaxFileSize,
	}
	storage := NewStorage(tgClient, storageConfig)

	server := NewS3Server(storage, config.Server, botState, config.Bot.Token, config.Bot.Admins)

	if config.Bot.Token != "" {
		go server.StartBot(context.Background())
	}

	addr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port)
	fmt.Printf("\n🌐 S3 API: http://%s\n", addr)
	fmt.Printf("🔑 Access Key: %s\n", config.Server.AccessKey)
	fmt.Printf("🔒 Secret Key: %s\n", config.Server.SecretKey)
	if config.Bot.Token != "" {
		fmt.Printf("🤖 Bot enabled\n")
	}

	if err := server.Start(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}