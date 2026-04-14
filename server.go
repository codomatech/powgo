package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"powgo/utils"
)

type Config struct {
	Difficulty       int               `json:"difficulty"`
	AllowedHexDigits string            `json:"allowedHexDigits"`
	RedisHost        string            `json:"redisHost"`
	RedisPort        string            `json:"redisPort"`
	Port             string            `json:"port"`
	AllowedOrigins   []string          `json:"allowedOrigins"` // optional list of allowed CORS origins
	Upstreams        map[string]string `json:"upstreams"`
}

var (
	rdb           *redis.Client
	difficulty    int
	allowedDigits string
	upstreams     map[string]*url.URL
	cfg           Config
)

func setPowConfigHeader(w http.ResponseWriter, token string) {
	w.Header().Set("x-powgo-config", fmt.Sprintf("session=%s; difficulty=%d; allowed=%s", token, difficulty, allowedDigits))
}

type session struct {
	IP           string  `json:"ip"`
	UserAgent    string  `json:"user_agent"`
	Attempts     []int64 `json:"attempts"`
	BlockedUntil int64   `json:"blocked_until"`
	SuccessCount int     `json:"success_count"`
	TokenVersion int     `json:"token_version"`
}

func loadConfig(configPath string) error {
	//#nosec G304 -- config path is operator-controlled via CLI flag
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config JSON: %v", err)
	}
	if cfg.Difficulty <= 0 {
		return fmt.Errorf("difficulty must be positive")
	}
	difficulty = cfg.Difficulty
	allowedDigits = cfg.AllowedHexDigits
	if allowedDigits == "" {
		return fmt.Errorf("allowedHexDigits cannot be empty")
	}
	if cfg.RedisHost == "" {
		cfg.RedisHost = "localhost"
	}
	if cfg.RedisPort == "" {
		cfg.RedisPort = "6379"
	}
	rdb = redis.NewClient(&redis.Options{
		Addr:     cfg.RedisHost + ":" + cfg.RedisPort,
		Password: "",
		DB:       0,
	})
	upstreams = make(map[string]*url.URL)
	for host, upstream := range cfg.Upstreams {
		parsed, err := url.Parse(upstream)
		if err != nil {
			return fmt.Errorf("invalid upstream URL for host %s: %v", host, err)
		}
		upstreams[host] = parsed
	}
	if len(upstreams) == 0 {
		return fmt.Errorf("at least one upstream must be configured")
	}
	return nil
}

func getUpstreamURL(host string) *url.URL {
	if upstreams == nil {
		return nil
	}
	if u, ok := upstreams[host]; ok {
		return u
	}
	if u, ok := upstreams["*"]; ok {
		return u
	}
	return nil
}

func generateSessionTokenBase(r *http.Request) string {
	ip := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		ip = strings.Split(forwarded, ",")[0]
	}
	userAgent := r.Header.Get("User-Agent")
	data := ip + userAgent
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func generateSessionToken(r *http.Request, version int) string {
	base := generateSessionTokenBase(r)
	return base + ":" + strconv.Itoa(version)
}

func parseTokenVersion(token string) (base string, version int) {
	parts := strings.Split(token, ":")
	if len(parts) == 1 {
		return parts[0], 0
	}
	v, err := strconv.Atoi(parts[1])
	if err != nil {
		return parts[0], 0
	}
	return parts[0], v
}

func getSessionByBase(base string) (*session, error) {
	ctx := context.Background()
	val, err := rdb.Get(ctx, "session:"+base).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal([]byte(val), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveSession(token string, s *session) error {
	base, _ := parseTokenVersion(token)
	ctx := context.Background()
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return rdb.Set(ctx, "session:"+base, data, time.Hour).Err()
}

func addAttempt(s *session) {
	now := time.Now().Unix()
	s.Attempts = append(s.Attempts, now)
	i := 0
	for ; i < len(s.Attempts); i++ {
		if s.Attempts[i] >= now-60 {
			break
		}
	}
	s.Attempts = s.Attempts[i:]
	if len(s.Attempts) >= 32 {
		s.BlockedUntil = now + 300
	}
}

func isBlocked(s *session) bool {
	if s == nil {
		return false
	}
	now := time.Now().Unix()
	if now >= s.BlockedUntil && s.BlockedUntil > 0 {
		s.BlockedUntil = 0
	}
	return now < s.BlockedUntil
}

func verifyNonce(data, nonce string) bool {
	return utils.VerifyProofOfWork(data, nonce, difficulty, allowedDigits)
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" && len(cfg.AllowedOrigins) > 0 {
			for _, allowed := range cfg.AllowedOrigins {
				if allowed == "*" {
					w.Header().Set("Access-Control-Allow-Origin", "*")
					// No Vary or Credentials when using wildcard
					break
				}
				if allowed == origin {
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					break
				}
			}
		}

		// Set these regardless — needed for preflight even if origin didn't match,
		// and harmless otherwise
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-powgo-nonce")
		w.Header().Set("Access-Control-Expose-Headers", "x-powgo-config")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	base := generateSessionTokenBase(r)
	sess, err := getSessionByBase(base)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if sess == nil {
		sess = &session{
			IP:           r.RemoteAddr,
			UserAgent:    r.Header.Get("User-Agent"),
			SuccessCount: 0,
			TokenVersion: 0,
		}
	}

	token := generateSessionToken(r, sess.TokenVersion)

	host := r.Host
	if colon := strings.Index(host, ":"); colon != -1 {
		host = host[:colon]
	}
	target := getUpstreamURL(host)
	if target == nil {
		http.Error(w, "No upstream configured for host", http.StatusNotFound)
		return
	}

	if isBlocked(sess) {
		setPowConfigHeader(w, token)
		http.Error(w, "Access blocked", http.StatusTooManyRequests)
		return
	}

	nonce := r.Header.Get("x-powgo-nonce")

	if nonce == "" {
		setPowConfigHeader(w, token)
		http.Error(w, "POWGO Nonce Required", 299)
		return
	}

	if !verifyNonce(token, nonce) {
		addAttempt(sess)
		err = saveSession(token, sess)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		setPowConfigHeader(w, token)
		status := 299
		if len(sess.Attempts) > 5 {
			status = http.StatusForbidden
		}
		http.Error(w, "Access denied", status)
		return
	}

	// Successful POW verification
	sess.SuccessCount++
	if sess.SuccessCount >= 50 {
		sess.TokenVersion++
		sess.SuccessCount = 0
	}
	sess.Attempts = nil
	sess.BlockedUntil = 0
	err = saveSession(token, sess)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// After possible version increment, recompute token
	token = generateSessionToken(r, sess.TokenVersion)
	setPowConfigHeader(w, token)

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config.json", "path to config file")
	flag.Parse()

	if err := loadConfig(configPath); err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	http.HandleFunc("/", corsMiddleware(handler))
	port := cfg.Port
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      nil,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  5 * time.Minute,
	}
	fmt.Printf("Starting server on :%s\n", port)
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}

}
