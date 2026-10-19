package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type User struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Email      string    `json:"email,omitempty"`
	Username   string    `json:"username"`
	Bio        string    `json:"bio"`
	Avatar     string    `json:"avatar,omitempty"`
	Password   string    `json:"-"`
	Verified   bool      `json:"emailVerified"`
	Onboarding bool      `json:"onboardingComplete"`
	CreatedAt  time.Time `json:"createdAt"`
}
type publicUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Bio      string `json:"bio"`
	Avatar   string `json:"avatar,omitempty"`
}
type postView struct {
	ID         string         `json:"id"`
	Caption    string         `json:"caption"`
	Image      string         `json:"image,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	Author     publicUser     `json:"author"`
	LikeCount  int            `json:"likeCount"`
	LikedByMe  bool           `json:"likedByMe"`
	ReplyCount int            `json:"replyCount"`
	SavedByMe  bool           `json:"savedByMe"`
	MyReaction string         `json:"myReaction"`
	Reactions  map[string]int `json:"reactions"`
}
type storyView struct {
	ID        string     `json:"id"`
	Caption   string     `json:"caption"`
	Image     string     `json:"image,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	Viewed    bool       `json:"viewed"`
	Author    publicUser `json:"author"`
}
type postReplyView struct {
	ID        string     `json:"id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	Author    publicUser `json:"author"`
}
type notificationView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	EntityID  string     `json:"entityId"`
	CreatedAt time.Time  `json:"createdAt"`
	ReadAt    *time.Time `json:"readAt"`
	Actor     publicUser `json:"actor"`
}
type server struct {
	db *sql.DB
}

//go:embed web
var embeddedWeb embed.FS

func main() {
	loadDotEnv()
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("VERCEL") == "1" {
			log.Fatal("MYSQL_DSN wajib diisi pada Vercel dan harus menunjuk ke MySQL yang dapat diakses dari internet.")
		}
		dsn = "root:@tcp(127.0.0.1:3306)/temanaja?parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci"
	}
	var db *sql.DB
	var err error
	if caPEM := os.Getenv("MYSQL_CA_CERT"); caPEM != "" {
		cfg, parseErr := mysql.ParseDSN(dsn)
		if parseErr != nil {
			log.Fatalf("MYSQL_DSN tidak valid: %v", parseErr)
		}
		host, _, splitErr := net.SplitHostPort(cfg.Addr)
		if splitErr != nil {
			log.Fatalf("Alamat host MYSQL_DSN tidak valid: %v", splitErr)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(caPEM)) {
			log.Fatal("MYSQL_CA_CERT bukan sertifikat PEM yang valid.")
		}
		cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host}
		connector, connectorErr := mysql.NewConnector(cfg)
		if connectorErr != nil {
			log.Fatalf("Konfigurasi koneksi MySQL gagal: %v", connectorErr)
		}
		db = sql.OpenDB(connector)
	} else {
		db, err = sql.Open("mysql", dsn)
	}
	if err != nil {
		log.Fatal(err)
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err = db.Ping(); err != nil {
		log.Fatalf("Tidak dapat terhubung ke MySQL: %v. Pastikan database temanaja sudah dibuat dan MYSQL_DSN benar.", err)
	}
	if err = initSchema(db); err != nil {
		log.Fatalf("Gagal menyiapkan tabel MySQL: %v", err)
	}
	s := &server{db: db}
	webFiles, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatal(err)
	}
	webHandler := http.FileServer(http.FS(webFiles))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("POST /api/register", s.register)
	mux.HandleFunc("POST /api/verify-email", s.verifyEmailCode)
	mux.HandleFunc("POST /api/resend-verification", s.resendVerificationCode)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/password-reset/request", s.requestPasswordReset)
	mux.HandleFunc("POST /api/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /verify", s.verifyEmail)
	mux.HandleFunc("GET /reset-password", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		serveEmbeddedPage(w, r, webFiles, "reset-password.html")
	})
	mux.HandleFunc("PUT /api/profile", s.updateProfile)
	mux.HandleFunc("POST /api/onboarding/complete", s.completeOnboarding)
	mux.HandleFunc("GET /api/users", s.users)
	mux.HandleFunc("GET /api/users/{id}", s.userProfile)
	mux.HandleFunc("GET /api/users/{id}/posts", s.userPosts)
	mux.HandleFunc("GET /api/friends", s.friends)
	mux.HandleFunc("DELETE /api/friends/{id}", s.removeFriend)
	mux.HandleFunc("GET /api/blocks", s.blocks)
	mux.HandleFunc("POST /api/blocks/{id}", s.blockUser)
	mux.HandleFunc("DELETE /api/blocks/{id}", s.unblockUser)
	mux.HandleFunc("GET /api/requests", s.requests)
	mux.HandleFunc("GET /api/notifications", s.notifications)
	mux.HandleFunc("POST /api/notifications/read-all", s.readAllNotifications)
	mux.HandleFunc("POST /api/notifications/{id}/read", s.readNotification)
	mux.HandleFunc("POST /api/requests", s.createRequest)
	mux.HandleFunc("POST /api/requests/{id}/accept", s.acceptRequest)
	mux.HandleFunc("POST /api/requests/{id}/decline", s.declineRequest)
	mux.HandleFunc("GET /api/posts", s.posts)
	mux.HandleFunc("GET /api/saved-posts", s.savedPosts)
	mux.HandleFunc("POST /api/posts", s.createPost)
	mux.HandleFunc("DELETE /api/posts/{id}", s.deletePost)
	mux.HandleFunc("PUT /api/posts/{id}", s.updatePost)
	mux.HandleFunc("PUT /api/posts/{id}/like", s.likePost)
	mux.HandleFunc("DELETE /api/posts/{id}/like", s.likePost)
	mux.HandleFunc("PUT /api/posts/{id}/reaction", s.reactPost)
	mux.HandleFunc("DELETE /api/posts/{id}/reaction", s.reactPost)
	mux.HandleFunc("GET /api/posts/{id}/replies", s.postReplies)
	mux.HandleFunc("POST /api/posts/{id}/replies", s.createPostReply)
	mux.HandleFunc("PUT /api/posts/{id}/save", s.savePost)
	mux.HandleFunc("DELETE /api/posts/{id}/save", s.savePost)
	mux.HandleFunc("GET /api/stories", s.stories)
	mux.HandleFunc("POST /api/stories", s.createStory)
	mux.HandleFunc("POST /api/stories/{id}/view", s.viewStory)
	mux.HandleFunc("DELETE /api/stories/{id}", s.deleteStory)
	mux.HandleFunc("GET /api/conversations", s.conversations)
	mux.HandleFunc("GET /api/messages", s.messages)
	mux.HandleFunc("POST /api/messages", s.sendMessage)
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		webHandler.ServeHTTP(w, r)
	})
	mux.Handle("/", webHandler)
	addr := env("ADDR", ":"+env("PORT", "8080"))
	log.Printf("gibahin.id berjalan di http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, securityHeaders(mux)))
}

func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			if current, exists := os.LookupEnv(key); !exists || current == "" {
				_ = os.Setenv(key, value)
			}
		}
	}
}

func env(key, fallback string) string {
	if x := os.Getenv(key); x != "" {
		return x
	}
	return fallback
}

func serveEmbeddedPage(w http.ResponseWriter, r *http.Request, webFiles fs.FS, name string) {
	data, err := fs.ReadFile(webFiles, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func initSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (id CHAR(32) PRIMARY KEY, name VARCHAR(100) NOT NULL, email VARCHAR(254) NOT NULL UNIQUE, username VARCHAR(24) NULL UNIQUE, bio VARCHAR(280) NOT NULL DEFAULT '', avatar MEDIUMTEXT NULL, password_hash VARCHAR(160) NOT NULL, email_verified BOOLEAN NOT NULL DEFAULT FALSE, onboarding_complete BOOLEAN NOT NULL DEFAULT FALSE, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX idx_users_name(name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS sessions (id CHAR(64) PRIMARY KEY, user_id CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), expires_at DATETIME(6) NOT NULL, INDEX idx_sessions_user(user_id), INDEX idx_sessions_expiry(expires_at), CONSTRAINT fk_session_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS email_verifications (user_id CHAR(32) PRIMARY KEY, token_hash CHAR(64) NOT NULL UNIQUE, expires_at DATETIME(6) NOT NULL, CONSTRAINT fk_verify_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS email_verification_meta (user_id CHAR(32) PRIMARY KEY, attempts TINYINT UNSIGNED NOT NULL DEFAULT 0, last_sent_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), CONSTRAINT fk_verify_meta_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS password_reset_tokens (user_id CHAR(32) PRIMARY KEY, token_hash CHAR(64) NOT NULL UNIQUE, expires_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), CONSTRAINT fk_password_reset_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS friendships (id CHAR(32) PRIMARY KEY, requester_id CHAR(32) NOT NULL, recipient_id CHAR(32) NOT NULL, state ENUM('pending','friends') NOT NULL DEFAULT 'pending', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), UNIQUE KEY uq_friend_direction(requester_id,recipient_id), INDEX idx_friend_recipient(recipient_id,state), CONSTRAINT fk_friend_requester FOREIGN KEY(requester_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_friend_recipient FOREIGN KEY(recipient_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS notifications (id CHAR(32) PRIMARY KEY, user_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL, kind ENUM('friend_request','friend_accepted','message','post','like','reply') NOT NULL, entity_id CHAR(32) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), read_at DATETIME(6) NULL, INDEX idx_notifications_user_date(user_id,created_at), INDEX idx_notifications_user_unread(user_id,read_at,created_at), CONSTRAINT fk_notification_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_notification_actor FOREIGN KEY(actor_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS posts (id CHAR(32) PRIMARY KEY, user_id CHAR(32) NOT NULL, caption VARCHAR(1000) NOT NULL DEFAULT '', image MEDIUMTEXT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX idx_posts_user_date(user_id,created_at), CONSTRAINT fk_post_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS saved_posts (user_id CHAR(32) NOT NULL, post_id CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), PRIMARY KEY(user_id,post_id), INDEX idx_saved_post(post_id), CONSTRAINT fk_saved_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_saved_post FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS post_reactions (post_id CHAR(32) NOT NULL, user_id CHAR(32) NOT NULL, reaction VARCHAR(16) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), PRIMARY KEY(post_id,user_id), INDEX idx_reaction_post(post_id,reaction), CONSTRAINT fk_reaction_post FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE, CONSTRAINT fk_reaction_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS stories (id CHAR(32) PRIMARY KEY, user_id CHAR(32) NOT NULL, caption VARCHAR(280) NOT NULL DEFAULT '', image MEDIUMTEXT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), expires_at DATETIME(6) NOT NULL, INDEX idx_story_user_expiry(user_id,expires_at), CONSTRAINT fk_story_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS story_views (story_id CHAR(32) NOT NULL, user_id CHAR(32) NOT NULL, viewed_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), PRIMARY KEY(story_id,user_id), INDEX idx_story_viewer(user_id), CONSTRAINT fk_story_view_story FOREIGN KEY(story_id) REFERENCES stories(id) ON DELETE CASCADE, CONSTRAINT fk_story_view_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS post_likes (post_id CHAR(32) NOT NULL, user_id CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), PRIMARY KEY(post_id,user_id), INDEX idx_post_likes_user(user_id), CONSTRAINT fk_post_like_post FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE, CONSTRAINT fk_post_like_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS post_replies (id CHAR(32) PRIMARY KEY, post_id CHAR(32) NOT NULL, user_id CHAR(32) NOT NULL, body VARCHAR(1000) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX idx_post_replies_date(post_id,created_at), CONSTRAINT fk_post_reply_post FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE, CONSTRAINT fk_post_reply_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS messages (id CHAR(32) PRIMARY KEY, sender_id CHAR(32) NOT NULL, recipient_id CHAR(32) NOT NULL, body VARCHAR(4000) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX idx_messages_pair_date(sender_id,recipient_id,created_at), INDEX idx_messages_reverse_date(recipient_id,sender_id,created_at), CONSTRAINT fk_message_sender FOREIGN KEY(sender_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_message_recipient FOREIGN KEY(recipient_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS conversation_reads (user_id CHAR(32) NOT NULL, peer_id CHAR(32) NOT NULL, last_read_at DATETIME(6) NOT NULL, PRIMARY KEY(user_id,peer_id), CONSTRAINT fk_conversation_read_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_conversation_read_peer FOREIGN KEY(peer_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS message_attachments (message_id CHAR(32) PRIMARY KEY, image MEDIUMTEXT NOT NULL, CONSTRAINT fk_attachment_message FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS blocked_users (blocker_id CHAR(32) NOT NULL, blocked_id CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), PRIMARY KEY(blocker_id,blocked_id), INDEX idx_blocked_id(blocked_id), CONSTRAINT fk_blocker_user FOREIGN KEY(blocker_id) REFERENCES users(id) ON DELETE CASCADE, CONSTRAINT fk_blocked_user FOREIGN KEY(blocked_id) REFERENCES users(id) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	_, err := db.Exec(`INSERT INTO notifications(id,user_id,actor_id,kind,entity_id,created_at) SELECT REPLACE(UUID(),'-',''),f.recipient_id,f.requester_id,'friend_request',f.id,f.created_at FROM friendships f WHERE f.state='pending' AND NOT EXISTS(SELECT 1 FROM notifications n WHERE n.user_id=f.recipient_id AND n.kind='friend_request' AND n.entity_id=f.id)`)
	if err != nil {
		return err
	}
	return nil
}
func (s *server) addNotification(userID, actorID, kind, entityID string) {
	if userID == "" || actorID == "" || userID == actorID {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO notifications(id,user_id,actor_id,kind,entity_id) VALUES(?,?,?,?,?)`, randomID(), userID, actorID, kind, entityID)
}

func (s *server) notifyPostFriends(ownerID, postID string) {
	rows, err := s.db.Query(`SELECT requester_id FROM friendships WHERE state='friends' AND recipient_id=? UNION SELECT recipient_id FROM friendships WHERE state='friends' AND requester_id=?`, ownerID, ownerID)
	if err != nil {
		return
	}
	friendIDs := []string{}
	for rows.Next() {
		var friendID string
		if rows.Scan(&friendID) == nil {
			friendIDs = append(friendIDs, friendID)
		}
	}
	_ = rows.Close()
	for _, friendID := range friendIDs {
		if !s.isBlocked(ownerID, friendID) {
			s.addNotification(friendID, ownerID, "post", postID)
		}
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(v)
}
func hmacSum(key string, msg []byte) []byte {
	block := []byte(key)
	if len(block) > 64 {
		sum := sha256.Sum256(block)
		block = sum[:]
	}
	k := make([]byte, 64)
	copy(k, block)
	inner := make([]byte, 64+len(msg))
	outer := make([]byte, 64+sha256.Size)
	for i := 0; i < 64; i++ {
		inner[i] = k[i] ^ 0x36
		outer[i] = k[i] ^ 0x5c
	}
	copy(inner[64:], msg)
	is := sha256.Sum256(inner)
	copy(outer[64:], is[:])
	os := sha256.Sum256(outer)
	return os[:]
}
func hashPassword(pw string, salt []byte) []byte {
	const rounds = 180000
	x := append(append([]byte{}, salt...), 0, 0, 0, 1)
	u := hmacSum(pw, x)
	out := append([]byte{}, u...)
	for i := 1; i < rounds; i++ {
		u = hmacSum(pw, u)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}
func passwordHash(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(hashPassword(pw, salt))
}
func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, e := hex.DecodeString(parts[0])
	if e != nil {
		return false
	}
	want, e := hex.DecodeString(parts[1])
	if e != nil {
		return false
	}
	got := hashPassword(pw, salt)
	return len(want) == len(got) && subtle.ConstantTimeCompare(want, got) == 1
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func newPasswordResetToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func appBaseURL(r *http.Request) string {
	if configured := strings.TrimSpace(env("APP_BASE_URL", os.Getenv("APP_URL"))); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	proto := "http"
	forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if r.TLS != nil || strings.EqualFold(forwardedProto, "https") {
		proto = "https"
	}
	return proto + "://" + r.Host
}

func (s *server) getUser(id string) (User, error) {
	var u User
	var username, avatar sql.NullString
	var verified, onboard bool
	err := s.db.QueryRow(`SELECT id,name,email,username,bio,avatar,password_hash,email_verified,onboarding_complete,created_at FROM users WHERE id=?`, id).Scan(&u.ID, &u.Name, &u.Email, &username, &u.Bio, &avatar, &u.Password, &verified, &onboard, &u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	u.Username = username.String
	u.Avatar = avatar.String
	u.Verified = verified
	u.Onboarding = onboard
	return u, nil
}
func safe(u User) publicUser { return publicUser{u.ID, u.Name, u.Username, u.Bio, u.Avatar} }
func (s *server) current(r *http.Request) (User, bool) {
	c, err := r.Cookie("temanaja_session")
	if err != nil {
		return User{}, false
	}
	var id string
	err = s.db.QueryRow(`SELECT user_id FROM sessions WHERE id=? AND expires_at>CURRENT_TIMESTAMP(6)`, tokenHash(c.Value)).Scan(&id)
	if err != nil {
		return User{}, false
	}
	u, err := s.getUser(id)
	return u, err == nil
}
func (s *server) setSession(w http.ResponseWriter, r *http.Request, u User) error {
	token := randomID() + randomID()
	if _, err := s.db.Exec(`INSERT INTO sessions(id,user_id,expires_at) VALUES(?,?,DATE_ADD(CURRENT_TIMESTAMP(6),INTERVAL 30 DAY))`, tokenHash(token), u.ID); err != nil {
		return err
	}
	secure := r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") || os.Getenv("VERCEL") == "1"
	http.SetCookie(w, &http.Cookie{Name: "temanaja_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 24 * 30})
	return nil
}
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	writeJSON(w, 200, u)
}

func (s *server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Data pendaftaran tidak valid.")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	parsedEmail, emailErr := mail.ParseAddress(in.Email)
	if len(in.Name) < 2 || len(in.Name) > 100 || len(in.Email) > 254 || emailErr != nil || parsedEmail.Address != in.Email || len(in.Password) < 8 {
		fail(w, 400, "Isi nama, email yang valid, dan kata sandi minimal 8 karakter.")
		return
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email=?`, in.Email).Scan(&exists); err != nil {
		fail(w, 500, "Tidak dapat memeriksa email.")
		return
	}
	if exists > 0 {
		fail(w, 409, "Email ini sudah terdaftar. Silakan masuk.")
		return
	}
	id := randomID()
	code, err := newVerificationCode()
	if err != nil {
		fail(w, 500, "Gagal membuat kode verifikasi.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Tidak dapat membuat akun.")
		return
	}
	_, err = tx.Exec(`INSERT INTO users(id,name,email,password_hash) VALUES(?,?,?,?)`, id, in.Name, in.Email, passwordHash(in.Password))
	if err == nil {
		_, err = tx.Exec(`INSERT INTO email_verifications(user_id,token_hash,expires_at) VALUES(?,?,DATE_ADD(NOW(6), INTERVAL 10 MINUTE))`, id, tokenHash(code))
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO email_verification_meta(user_id,attempts,last_sent_at) VALUES(?,0,NOW(6))`, id)
	}
	if err != nil {
		_ = tx.Rollback()
		fail(w, 500, "Akun belum dapat dibuat.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Akun belum dapat disimpan.")
		return
	}
	sent := true
	deliveryError := ""
	if err = sendVerificationEmail(in.Email, in.Name, code); err != nil {
		sent = false
		deliveryError = verificationEmailDeliveryHint(err)
		log.Printf("Gagal mengirim kode verifikasi ke %s: %v", in.Email, err)
	}
	writeJSON(w, 201, map[string]any{"message": "Akun dibuat.", "email": in.Email, "sent": sent, "deliveryError": deliveryError})
}
func newVerificationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

func sendVerificationEmail(to, name, code string) error {
	if os.Getenv("SMTP_HOST") == "" {
		log.Printf("MODE DEVELOPMENT: kode verifikasi untuk %s (%s): %s", name, to, code)
		return errors.New("SMTP_HOST kosong; kode dicetak di terminal untuk development")
	}
	body := fmt.Sprintf("Hai %s,\r\n\r\nKode verifikasi email gibahin.id kamu adalah: %s\r\n\r\nMasukkan kode ini di halaman pendaftaran. Kode berlaku selama 10 menit. Jika kamu tidak merasa mendaftar, abaikan email ini.\r\n", name, code)
	return sendGibahinEmail(to, "Kode verifikasi gibahin.id", body)
}

func sendPasswordResetEmail(to, name, resetLink string) error {
	body := fmt.Sprintf("Hai %s,\r\n\r\nKami menerima permintaan untuk mengatur ulang kata sandi akun gibahin.id kamu. Buka tautan berikut untuk membuat kata sandi baru:\r\n\r\n%s\r\n\r\nTautan berlaku selama 30 menit dan hanya dapat digunakan sekali. Jika kamu tidak meminta pengaturan ulang kata sandi, abaikan email ini.\r\n", name, resetLink)
	return sendGibahinEmail(to, "Atur ulang kata sandi gibahin.id", body)
}

func sendGibahinEmail(to, subject, body string) error {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		return errors.New("SMTP_HOST kosong")
	}
	port := env("SMTP_PORT", "587")
	from := env("SMTP_FROM", os.Getenv("SMTP_USERNAME"))
	username := os.Getenv("SMTP_USERNAME")
	password := os.Getenv("SMTP_PASSWORD")
	if from == "" {
		return errors.New("SMTP_FROM atau SMTP_USERNAME perlu diisi")
	}
	if username != "" && password == "" {
		return errors.New("SMTP_PASSWORD perlu diisi jika SMTP_USERNAME digunakan")
	}
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return fmt.Errorf("SMTP_FROM tidak valid: %w", err)
	}
	message := fmt.Sprintf("To: %s\r\nFrom: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", to, sender.String(), subject, body)
	return smtp.SendMail(host+":"+port, auth, sender.Address, []string{to}, []byte(message))
}

func verificationEmailDeliveryHint(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "535") || strings.Contains(message, "badcredentials") || strings.Contains(message, "username and password not accepted") {
		return "Gmail menolak autentikasi SMTP. Periksa SMTP_USERNAME (alamat Gmail lengkap) dan gunakan App Password Google, bukan password akun Gmail."
	}
	return "Email belum terkirim. Periksa konfigurasi dan koneksi SMTP, lalu kirim ulang kode."
}

func (s *server) verifyEmailCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Masukkan email dan kode verifikasi.")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if len(in.Code) != 4 || strings.Trim(in.Code, "0123456789") != "" {
		fail(w, 400, "Kode harus terdiri dari 4 angka.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "Verifikasi belum dapat diproses.")
		return
	}
	defer tx.Rollback()
	var id, expectedHash string
	err = tx.QueryRowContext(r.Context(), `SELECT v.user_id,v.token_hash FROM email_verifications v JOIN users u ON u.id=v.user_id WHERE u.email=? AND v.expires_at>NOW(6) AND u.email_verified=FALSE FOR UPDATE`, in.Email).Scan(&id, &expectedHash)
	if err != nil {
		fail(w, 400, "Kode tidak valid atau sudah kedaluwarsa. Kirim kode baru.")
		return
	}
	var attempts int
	if err = tx.QueryRowContext(r.Context(), `SELECT attempts FROM email_verification_meta WHERE user_id=? FOR UPDATE`, id).Scan(&attempts); err != nil {
		fail(w, 500, "Status verifikasi belum dapat dibaca.")
		return
	}
	if attempts >= 5 {
		fail(w, 429, "Batas percobaan tercapai. Kirim kode baru untuk melanjutkan.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(tokenHash(in.Code))) != 1 {
		_, _ = tx.ExecContext(r.Context(), `UPDATE email_verification_meta SET attempts=attempts+1 WHERE user_id=?`, id)
		_ = tx.Commit()
		fail(w, 400, "Kode verifikasi salah.")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET email_verified=TRUE WHERE id=?`, id); err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM email_verifications WHERE user_id=?`, id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM email_verification_meta WHERE user_id=?`, id)
	}
	if err != nil {
		fail(w, 500, "Verifikasi belum dapat disimpan.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Verifikasi belum dapat disimpan.")
		return
	}
	u, err := s.getUser(id)
	if err != nil {
		fail(w, 500, "Akun belum dapat dimuat.")
		return
	}
	if err := s.setSession(w, r, u); err != nil {
		fail(w, 500, "Sesi masuk belum dapat dibuat.")
		return
	}
	writeJSON(w, 200, u)
}

func (s *server) resendVerificationCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Masukkan email pendaftaran.")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "Kode baru belum dapat disiapkan.")
		return
	}
	defer tx.Rollback()
	var id, name string
	var verified bool
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,email_verified FROM users WHERE email=? FOR UPDATE`, in.Email).Scan(&id, &name, &verified)
	if err == sql.ErrNoRows {
		fail(w, 404, "Email pendaftaran tidak ditemukan.")
		return
	}
	if err != nil {
		fail(w, 500, "Akun belum dapat diperiksa.")
		return
	}
	if verified {
		fail(w, 409, "Email ini sudah diverifikasi. Silakan masuk.")
		return
	}
	var lastSent time.Time
	metaErr := tx.QueryRowContext(r.Context(), `SELECT last_sent_at FROM email_verification_meta WHERE user_id=? FOR UPDATE`, id).Scan(&lastSent)
	if metaErr == nil && time.Since(lastSent) < 30*time.Second {
		seconds := int((30*time.Second - time.Since(lastSent)).Seconds()) + 1
		fail(w, 429, fmt.Sprintf("Tunggu %d detik sebelum meminta kode baru.", seconds))
		return
	}
	if metaErr != nil && metaErr != sql.ErrNoRows {
		fail(w, 500, "Status pengiriman kode belum dapat dibaca.")
		return
	}
	code, err := newVerificationCode()
	if err != nil {
		fail(w, 500, "Gagal membuat kode verifikasi.")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO email_verifications(user_id,token_hash,expires_at) VALUES(?,?,DATE_ADD(NOW(6), INTERVAL 10 MINUTE)) ON DUPLICATE KEY UPDATE token_hash=VALUES(token_hash),expires_at=VALUES(expires_at)`, id, tokenHash(code))
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO email_verification_meta(user_id,attempts,last_sent_at) VALUES(?,0,NOW(6)) ON DUPLICATE KEY UPDATE attempts=0,last_sent_at=NOW(6)`, id)
	}
	if err != nil {
		fail(w, 500, "Kode baru belum dapat disimpan.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Kode baru belum dapat disimpan.")
		return
	}
	if err = sendVerificationEmail(in.Email, name, code); err != nil {
		log.Printf("Gagal mengirim ulang kode verifikasi ke %s: %v", in.Email, err)
		fail(w, 502, verificationEmailDeliveryHint(err))
		return
	}
	writeJSON(w, 200, map[string]any{"message": "Kode verifikasi baru sudah dikirim.", "email": in.Email, "sent": true})
}

func (s *server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/?verified=invalid", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "Verifikasi belum dapat diproses.", 500)
		return
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM email_verifications WHERE token_hash=? AND expires_at>NOW()`, tokenHash(token)).Scan(&id)
	if err != nil {
		_ = tx.Rollback()
		http.Redirect(w, r, "/?verified=expired", http.StatusSeeOther)
		return
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET email_verified=TRUE WHERE id=?`, id); err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM email_verifications WHERE user_id=?`, id)
	}
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "Verifikasi belum dapat disimpan.", 500)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Verifikasi belum dapat disimpan.", 500)
		return
	}
	u, err := s.getUser(id)
	if err != nil {
		http.Error(w, "Akun belum dapat dimuat.", 500)
		return
	}
	if err := s.setSession(w, r, u); err != nil {
		http.Error(w, "Sesi masuk belum dapat dibuat.", 500)
		return
	}
	http.Redirect(w, r, "/?verified=success", http.StatusSeeOther)
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Data masuk tidak valid.")
		return
	}
	var u User
	var username, avatar sql.NullString
	var verified, onboard bool
	err := s.db.QueryRow(`SELECT id,name,email,username,bio,avatar,password_hash,email_verified,onboarding_complete,created_at FROM users WHERE email=?`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&u.ID, &u.Name, &u.Email, &username, &u.Bio, &avatar, &u.Password, &verified, &onboard, &u.CreatedAt)
	if err != nil || !verifyPassword(u.Password, in.Password) {
		fail(w, 401, "Email atau kata sandi salah.")
		return
	}
	if !verified {
		fail(w, 403, "Emailmu belum diverifikasi. Buka tautan verifikasi yang kami kirim.")
		return
	}
	u.Username = username.String
	u.Avatar = avatar.String
	u.Verified = verified
	u.Onboarding = onboard
	if err := s.setSession(w, r, u); err != nil {
		fail(w, 500, "Sesi masuk belum dapat dibuat.")
		return
	}
	writeJSON(w, 200, u)
}

func (s *server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	const genericMessage = "Jika email terdaftar, tautan untuk mengatur ulang kata sandi akan dikirim. Periksa juga folder spam."
	var in struct {
		Email string `json:"email"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Email tidak valid.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || len(email) > 254 {
		writeJSON(w, 200, map[string]string{"message": genericMessage})
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "Permintaan reset belum dapat diproses.")
		return
	}
	defer tx.Rollback()
	var userID, name string
	var verified bool
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,email_verified FROM users WHERE email=? FOR UPDATE`, email).Scan(&userID, &name, &verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !verified) {
		writeJSON(w, 200, map[string]string{"message": genericMessage})
		return
	}
	if err != nil {
		fail(w, 500, "Permintaan reset belum dapat diproses.")
		return
	}
	var lastRequested time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT created_at FROM password_reset_tokens WHERE user_id=? FOR UPDATE`, userID).Scan(&lastRequested)
	if err == nil && time.Since(lastRequested) < time.Minute {
		writeJSON(w, 200, map[string]string{"message": genericMessage})
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "Permintaan reset belum dapat diproses.")
		return
	}
	token, err := newPasswordResetToken()
	if err != nil {
		fail(w, 500, "Tautan reset belum dapat dibuat.")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO password_reset_tokens(user_id,token_hash,expires_at,created_at) VALUES(?,?,DATE_ADD(NOW(6), INTERVAL 30 MINUTE),NOW(6)) ON DUPLICATE KEY UPDATE token_hash=VALUES(token_hash),expires_at=VALUES(expires_at),created_at=NOW(6)`, userID, tokenHash(token))
	if err != nil {
		fail(w, 500, "Tautan reset belum dapat disimpan.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Permintaan reset belum dapat diproses.")
		return
	}
	link := appBaseURL(r) + "/reset-password?token=" + token
	if err = sendPasswordResetEmail(email, name, link); err != nil {
		log.Printf("Gagal mengirim tautan reset password: %v", err)
		_, _ = s.db.ExecContext(r.Context(), `DELETE FROM password_reset_tokens WHERE user_id=? AND token_hash=?`, userID, tokenHash(token))
	}
	writeJSON(w, 200, map[string]string{"message": genericMessage})
}

func (s *server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token           string `json:"token"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirmPassword"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Data reset tidak valid.")
		return
	}
	if in.Token == "" {
		fail(w, 400, "Tautan reset tidak valid. Minta tautan yang baru.")
		return
	}
	if len([]rune(in.Password)) < 8 || len([]rune(in.Password)) > 128 {
		fail(w, 400, "Kata sandi baru harus berisi 8 sampai 128 karakter.")
		return
	}
	if in.Password != in.ConfirmPassword {
		fail(w, 400, "Kedua kata sandi belum sama.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "Kata sandi belum dapat diperbarui.")
		return
	}
	defer tx.Rollback()
	var userID string
	err = tx.QueryRowContext(r.Context(), `SELECT user_id FROM password_reset_tokens WHERE token_hash=? AND expires_at>NOW(6) FOR UPDATE`, tokenHash(in.Token)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 410, "Tautan reset sudah kedaluwarsa atau pernah digunakan. Minta tautan baru.")
		return
	}
	if err != nil {
		fail(w, 500, "Tautan reset belum dapat diperiksa.")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET password_hash=? WHERE id=?`, passwordHash(in.Password), userID); err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM password_reset_tokens WHERE user_id=?`, userID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE user_id=?`, userID)
	}
	if err != nil {
		fail(w, 500, "Kata sandi belum dapat diperbarui.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Kata sandi belum dapat diperbarui.")
		return
	}
	writeJSON(w, 200, map[string]string{"message": "Kata sandi berhasil diatur ulang. Silakan masuk dengan kata sandi baru."})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("temanaja_session"); e == nil {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id=?`, tokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "temanaja_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) updateProfile(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	var in struct {
		Name     *string `json:"name"`
		Username *string `json:"username"`
		Bio      *string `json:"bio"`
		Avatar   *string `json:"avatar"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Data profil tidak valid.")
		return
	}
	name, username, bio, avatar := me.Name, me.Username, me.Bio, me.Avatar
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if len([]rune(name)) < 2 || len([]rune(name)) > 100 {
			fail(w, 400, "Nama harus terdiri dari 2 sampai 100 karakter.")
			return
		}
	}
	if in.Username != nil {
		username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(*in.Username, "@")))
		if len(username) < 3 || len(username) > 24 || !validUsername(username) {
			fail(w, 400, "Username harus 3 sampai 24 karakter: huruf, angka, titik, atau garis bawah.")
			return
		}
	}
	if in.Bio != nil {
		bio = strings.TrimSpace(*in.Bio)
		if len([]rune(bio)) > 280 {
			fail(w, 400, "Bio maksimal 280 karakter.")
			return
		}
	}
	if in.Avatar != nil {
		avatar = *in.Avatar
		if avatar != "" {
			parts := strings.SplitN(avatar, ",", 2)
			if len(parts) != 2 || !(strings.HasPrefix(parts[0], "data:image/jpeg;base64") || strings.HasPrefix(parts[0], "data:image/png;base64") || strings.HasPrefix(parts[0], "data:image/webp;base64")) {
				fail(w, 400, "Gunakan foto JPEG, PNG, atau WebP.")
				return
			}
			raw, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil || len(raw) > 3<<20 {
				fail(w, 400, "Foto profil maksimal 3 MB.")
				return
			}
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET name=?,username=?,bio=?,avatar=? WHERE id=?`, name, nullString(username), bio, nullString(avatar), me.ID); err != nil {
		fail(w, 409, "Username sudah digunakan atau profil tidak dapat disimpan.")
		return
	}
	u, err := s.getUser(me.ID)
	if err != nil {
		fail(w, 500, "Profil tidak dapat dimuat.")
		return
	}
	writeJSON(w, 200, u)
}
func validUsername(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	var username string
	var avatar sql.NullString
	err := s.db.QueryRow(`SELECT username,avatar FROM users WHERE id=?`, me.ID).Scan(&username, &avatar)
	if err != nil || username == "" || !avatar.Valid || avatar.String == "" {
		fail(w, 400, "Pilih username dan pasang foto profil sebelum melanjutkan.")
		return
	}
	if _, err = s.db.Exec(`UPDATE users SET onboarding_complete=TRUE WHERE id=?`, me.ID); err != nil {
		fail(w, 500, "Onboarding belum tersimpan.")
		return
	}
	u, err := s.getUser(me.ID)
	if err != nil {
		fail(w, 500, "Profil tidak dapat dimuat.")
		return
	}
	writeJSON(w, 200, u)
}

func (s *server) users(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	q := "%" + strings.TrimSpace(strings.TrimPrefix(r.URL.Query().Get("q"), "@")) + "%"
	rows, err := s.db.Query(`SELECT id,name,username,bio,avatar FROM users WHERE email_verified=TRUE AND onboarding_complete=TRUE AND id<>? AND (name LIKE ? OR username LIKE ?) AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=users.id) OR (b.blocker_id=users.id AND b.blocked_id=?)) ORDER BY name LIMIT 50`, me.ID, q, q, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Pencarian akun gagal.")
		return
	}
	defer rows.Close()
	type result struct {
		User   publicUser `json:"user"`
		Status string     `json:"status"`
	}
	out := []result{}
	for rows.Next() {
		var u publicUser
		var avatar sql.NullString
		if err = rows.Scan(&u.ID, &u.Name, &u.Username, &u.Bio, &avatar); err != nil {
			continue
		}
		u.Avatar = avatar.String
		status := "none"
		var state, requester string
		e := s.db.QueryRow(`SELECT state,requester_id FROM friendships WHERE (requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?) LIMIT 1`, me.ID, u.ID, u.ID, me.ID).Scan(&state, &requester)
		if e == nil {
			if state == "friends" {
				status = "friends"
			} else if requester == me.ID {
				status = "sent"
			} else {
				status = "received"
			}
		}
		out = append(out, result{u, status})
	}
	writeJSON(w, 200, out)
}
func (s *server) userProfile(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	id := r.PathValue("id")
	if id != me.ID && !s.isFriend(me.ID, id) {
		fail(w, 404, "Profil teman tidak ditemukan.")
		return
	}
	u, err := s.getUser(id)
	if err != nil || !u.Verified || !u.Onboarding {
		fail(w, 404, "Profil teman tidak ditemukan.")
		return
	}
	writeJSON(w, 200, safe(u))
}

func (s *server) userPosts(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	id := r.PathValue("id")
	if id != me.ID && !s.isFriend(me.ID, id) {
		fail(w, 404, "Postingan teman tidak ditemukan.")
		return
	}
	rows, err := s.db.Query(postSelect+` WHERE p.user_id=? ORDER BY p.created_at DESC LIMIT 100`, me.ID, me.ID, me.ID, id)
	if err != nil {
		fail(w, 500, "Profil teman belum dapat dimuat.")
		return
	}
	defer rows.Close()
	out := []postView{}
	for rows.Next() {
		var p postView
		var image, avatar sql.NullString
		var u publicUser
		var heart, laugh, care, wow, sad int
		if rows.Scan(&p.ID, &p.Caption, &image, &p.CreatedAt, &u.ID, &u.Name, &u.Username, &u.Bio, &avatar, &p.LikeCount, &p.LikedByMe, &p.ReplyCount, &p.SavedByMe, &p.MyReaction, &heart, &laugh, &care, &wow, &sad) == nil {
			p.Image = image.String
			u.Avatar = avatar.String
			p.Author = u
			p.Reactions = map[string]int{"heart": heart, "laugh": laugh, "care": care, "wow": wow, "sad": sad}
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
}

func (s *server) friends(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(`SELECT u.id,u.name,u.username,u.bio,u.avatar FROM friendships f JOIN users u ON u.id=IF(f.requester_id=?,f.recipient_id,f.requester_id) WHERE (f.requester_id=? OR f.recipient_id=?) AND f.state='friends' AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=u.id) OR (b.blocker_id=u.id AND b.blocked_id=?)) ORDER BY u.name`, me.ID, me.ID, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Daftar teman gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []publicUser{}
	for rows.Next() {
		var u publicUser
		var avatar sql.NullString
		if rows.Scan(&u.ID, &u.Name, &u.Username, &u.Bio, &avatar) == nil {
			u.Avatar = avatar.String
			out = append(out, u)
		}
	}
	writeJSON(w, 200, out)
}

func (s *server) removeFriend(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	other := r.PathValue("id")
	res, err := s.db.Exec(`DELETE FROM friendships WHERE state='friends' AND ((requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?))`, me.ID, other, other, me.ID)
	if err != nil {
		fail(w, 500, "Teman belum dapat dihapus.")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "Teman tidak ditemukan.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) blocks(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(`SELECT u.id,u.name,u.username,u.bio,u.avatar FROM blocked_users b JOIN users u ON u.id=b.blocked_id WHERE b.blocker_id=? ORDER BY b.created_at DESC`, me.ID)
	if err != nil {
		fail(w, 500, "Daftar akun yang diblokir gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []publicUser{}
	for rows.Next() {
		var u publicUser
		var avatar sql.NullString
		if rows.Scan(&u.ID, &u.Name, &u.Username, &u.Bio, &avatar) == nil {
			u.Avatar = avatar.String
			out = append(out, u)
		}
	}
	writeJSON(w, 200, out)
}

func (s *server) blockUser(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	other := r.PathValue("id")
	if other == me.ID {
		fail(w, 400, "Kamu tidak bisa memblokir akun sendiri.")
		return
	}
	var exists bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE id=?)`, other).Scan(&exists); err != nil || !exists {
		fail(w, 404, "Akun tidak ditemukan.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Akun belum dapat diblokir.")
		return
	}
	if _, err = tx.Exec(`INSERT INTO blocked_users(blocker_id,blocked_id) VALUES(?,?) ON DUPLICATE KEY UPDATE created_at=CURRENT_TIMESTAMP(6)`, me.ID, other); err == nil {
		_, err = tx.Exec(`DELETE FROM friendships WHERE (requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?)`, me.ID, other, other, me.ID)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM friendships WHERE state='pending' AND ((requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?))`, me.ID, other, other, me.ID)
	}
	if err != nil {
		_ = tx.Rollback()
		fail(w, 500, "Akun belum dapat diblokir.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Akun belum dapat diblokir.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) unblockUser(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	res, err := s.db.Exec(`DELETE FROM blocked_users WHERE blocker_id=? AND blocked_id=?`, me.ID, r.PathValue("id"))
	if err != nil {
		fail(w, 500, "Akun belum dapat dibuka blokirnya.")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "Akun tidak ada dalam daftar blokir.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) requests(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(`SELECT f.id,u.id,u.name,u.username,u.bio,u.avatar FROM friendships f JOIN users u ON u.id=f.requester_id WHERE f.recipient_id=? AND f.state='pending' AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=u.id) OR (b.blocker_id=u.id AND b.blocked_id=?)) ORDER BY f.created_at DESC`, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Permintaan teman gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id string
		var u publicUser
		var avatar sql.NullString
		if rows.Scan(&id, &u.ID, &u.Name, &u.Username, &u.Bio, &avatar) == nil {
			u.Avatar = avatar.String
			out = append(out, map[string]any{"id": id, "user": u})
		}
	}
	writeJSON(w, 200, out)
}
func (s *server) notifications(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(`SELECT n.id,n.kind,n.entity_id,n.created_at,n.read_at,u.id,u.name,u.username,u.bio,u.avatar FROM notifications n JOIN users u ON u.id=n.actor_id WHERE n.user_id=? AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=n.user_id AND b.blocked_id=n.actor_id) OR (b.blocker_id=n.actor_id AND b.blocked_id=n.user_id)) ORDER BY n.created_at DESC LIMIT 50`, me.ID)
	if err != nil {
		fail(w, 500, "Notifikasi gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []notificationView{}
	for rows.Next() {
		var item notificationView
		var readAt sql.NullTime
		var avatar sql.NullString
		if err := rows.Scan(&item.ID, &item.Kind, &item.EntityID, &item.CreatedAt, &readAt, &item.Actor.ID, &item.Actor.Name, &item.Actor.Username, &item.Actor.Bio, &avatar); err != nil {
			fail(w, 500, "Notifikasi gagal dimuat.")
			return
		}
		item.Actor.Avatar = avatar.String
		if readAt.Valid {
			item.ReadAt = &readAt.Time
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "Notifikasi gagal dimuat.")
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) readNotification(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	if _, err := s.db.Exec(`UPDATE notifications SET read_at=COALESCE(read_at,CURRENT_TIMESTAMP(6)) WHERE id=? AND user_id=?`, r.PathValue("id"), me.ID); err != nil {
		fail(w, 500, "Status notifikasi gagal diperbarui.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	if _, err := s.db.Exec(`UPDATE notifications SET read_at=CURRENT_TIMESTAMP(6) WHERE user_id=? AND read_at IS NULL`, me.ID); err != nil {
		fail(w, 500, "Status notifikasi gagal diperbarui.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) createRequest(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	var in struct {
		UserID string `json:"userId"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Permintaan tidak valid.")
		return
	}
	if in.UserID == me.ID {
		fail(w, 400, "Kamu tidak bisa menambahkan diri sendiri.")
		return
	}
	if s.isBlocked(me.ID, in.UserID) {
		fail(w, 403, "Permintaan tidak tersedia untuk akun ini.")
		return
	}
	var ready bool
	if err := s.db.QueryRow(`SELECT email_verified AND onboarding_complete FROM users WHERE id=?`, in.UserID).Scan(&ready); err != nil || !ready {
		fail(w, 404, "Akun tidak ditemukan.")
		return
	}
	var state string
	var requester string
	err := s.db.QueryRow(`SELECT state,requester_id FROM friendships WHERE (requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?) LIMIT 1`, me.ID, in.UserID, in.UserID, me.ID).Scan(&state, &requester)
	if err == nil {
		if state == "pending" && requester != me.ID {
			if _, updateErr := s.db.Exec(`UPDATE friendships SET state='friends' WHERE requester_id=? AND recipient_id=?`, requester, me.ID); updateErr == nil {
				s.addNotification(requester, me.ID, "friend_accepted", "")
				_, _ = s.db.Exec(`UPDATE notifications SET read_at=CURRENT_TIMESTAMP(6) WHERE user_id=? AND actor_id=? AND kind='friend_request' AND read_at IS NULL`, me.ID, requester)
			}
			writeJSON(w, 200, map[string]string{"status": "friends"})
			return
		}
		fail(w, 409, "Permintaan pertemanan sudah ada.")
		return
	}
	requestID := randomID()
	if _, err = s.db.Exec(`INSERT INTO friendships(id,requester_id,recipient_id,state) VALUES(?,?,?,'pending')`, requestID, me.ID, in.UserID); err != nil {
		fail(w, 500, "Permintaan pertemanan gagal dikirim.")
		return
	}
	s.addNotification(in.UserID, me.ID, "friend_request", requestID)
	writeJSON(w, 201, map[string]string{"status": "sent"})
}
func (s *server) acceptRequest(w http.ResponseWriter, r *http.Request) {
	s.changeRequest(w, r, "friends")
}
func (s *server) declineRequest(w http.ResponseWriter, r *http.Request) {
	s.changeRequest(w, r, "declined")
}
func (s *server) changeRequest(w http.ResponseWriter, r *http.Request, state string) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	if state == "declined" {
		res, err := s.db.Exec(`DELETE FROM friendships WHERE id=? AND recipient_id=? AND state='pending'`, r.PathValue("id"), me.ID)
		if err != nil {
			fail(w, 500, "Permintaan belum dapat dihapus.")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(w, 404, "Permintaan tidak ditemukan.")
			return
		}
	} else {
		var requesterID string
		if err := s.db.QueryRow(`SELECT requester_id FROM friendships WHERE id=? AND recipient_id=? AND state='pending'`, r.PathValue("id"), me.ID).Scan(&requesterID); err != nil {
			fail(w, 404, "Permintaan tidak ditemukan.")
			return
		}
		res, err := s.db.Exec(`UPDATE friendships SET state='friends' WHERE id=? AND recipient_id=? AND state='pending'`, r.PathValue("id"), me.ID)
		if err != nil {
			fail(w, 500, "Permintaan belum dapat diterima.")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			fail(w, 404, "Permintaan tidak ditemukan.")
			return
		}
		s.addNotification(requesterID, me.ID, "friend_accepted", "")
	}
	_, _ = s.db.Exec(`UPDATE notifications SET read_at=COALESCE(read_at,CURRENT_TIMESTAMP(6)) WHERE user_id=? AND kind='friend_request' AND entity_id=? AND read_at IS NULL`, me.ID, r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": state})
}
func (s *server) isFriend(a, b string) bool {
	if s.isBlocked(a, b) {
		return false
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM friendships WHERE state='friends' AND ((requester_id=? AND recipient_id=?) OR (requester_id=? AND recipient_id=?))`, a, b, b, a).Scan(&n)
	return err == nil && n > 0
}

func (s *server) isBlocked(a, b string) bool {
	var blocked bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM blocked_users WHERE (blocker_id=? AND blocked_id=?) OR (blocker_id=? AND blocked_id=?))`, a, b, b, a).Scan(&blocked)
	return err == nil && blocked
}

const postSelect = `SELECT p.id,p.caption,p.image,p.created_at,u.id,u.name,u.username,u.bio,u.avatar,(SELECT COUNT(*) FROM post_likes pl WHERE pl.post_id=p.id),EXISTS(SELECT 1 FROM post_likes mine WHERE mine.post_id=p.id AND mine.user_id=?),(SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id=p.id),EXISTS(SELECT 1 FROM saved_posts sp WHERE sp.post_id=p.id AND sp.user_id=?),COALESCE((SELECT reaction FROM post_reactions mr WHERE mr.post_id=p.id AND mr.user_id=?),''),(SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.reaction='heart'),(SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.reaction='laugh'),(SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.reaction='care'),(SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.reaction='wow'),(SELECT COUNT(*) FROM post_reactions r WHERE r.post_id=p.id AND r.reaction='sad') FROM posts p JOIN users u ON u.id=p.user_id`

func (s *server) posts(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(postSelect+` WHERE (p.user_id=? OR EXISTS(SELECT 1 FROM friendships f WHERE f.state='friends' AND ((f.requester_id=? AND f.recipient_id=p.user_id) OR (f.recipient_id=? AND f.requester_id=p.user_id)))) AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=p.user_id) OR (b.blocker_id=p.user_id AND b.blocked_id=?)) ORDER BY p.created_at DESC LIMIT 100`, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Feed gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []postView{}
	for rows.Next() {
		var p postView
		var image, avatar sql.NullString
		var u publicUser
		var heart, laugh, care, wow, sad int
		if rows.Scan(&p.ID, &p.Caption, &image, &p.CreatedAt, &u.ID, &u.Name, &u.Username, &u.Bio, &avatar, &p.LikeCount, &p.LikedByMe, &p.ReplyCount, &p.SavedByMe, &p.MyReaction, &heart, &laugh, &care, &wow, &sad) == nil {
			p.Image = image.String
			u.Avatar = avatar.String
			p.Author = u
			p.Reactions = map[string]int{"heart": heart, "laugh": laugh, "care": care, "wow": wow, "sad": sad}
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
}
func (s *server) createPost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	if !me.Onboarding {
		fail(w, 403, "Lengkapi profil terlebih dahulu.")
		return
	}
	var in struct {
		Caption string `json:"caption"`
		Image   string `json:"image"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Postingan tidak valid.")
		return
	}
	in.Caption = strings.TrimSpace(in.Caption)
	if (in.Caption == "" && in.Image == "") || len([]rune(in.Caption)) > 1000 {
		fail(w, 400, "Tambahkan cerita atau foto. Cerita maksimal 1000 karakter.")
		return
	}
	if in.Image != "" {
		parts := strings.SplitN(in.Image, ",", 2)
		if len(parts) != 2 || !(strings.HasPrefix(parts[0], "data:image/jpeg;base64") || strings.HasPrefix(parts[0], "data:image/png;base64") || strings.HasPrefix(parts[0], "data:image/webp;base64")) {
			fail(w, 400, "Gunakan foto JPEG, PNG, atau WebP.")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil || len(raw) > 3<<20 {
			fail(w, 400, "Ukuran foto maksimal 3 MB.")
			return
		}
	}
	id := randomID()
	if _, err := s.db.Exec(`INSERT INTO posts(id,user_id,caption,image) VALUES(?,?,?,?)`, id, me.ID, in.Caption, nullString(in.Image)); err != nil {
		fail(w, 500, "Postingan belum bisa disimpan.")
		return
	}
	s.notifyPostFriends(me.ID, id)
	writeJSON(w, 201, postView{ID: id, Caption: in.Caption, Image: in.Image, CreatedAt: time.Now(), Author: safe(me)})
}

func (s *server) canViewPost(userID, postID string) bool {
	var authorID string
	err := s.db.QueryRow(`SELECT user_id FROM posts WHERE id=?`, postID).Scan(&authorID)
	if err != nil {
		return false
	}
	return authorID == userID || (s.isFriend(userID, authorID) && !s.isBlocked(userID, authorID))
}

func (s *server) savedPosts(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(postSelect+` JOIN saved_posts mine ON mine.post_id=p.id AND mine.user_id=? WHERE (p.user_id=? OR EXISTS(SELECT 1 FROM friendships f WHERE f.state='friends' AND ((f.requester_id=? AND f.recipient_id=p.user_id) OR (f.recipient_id=? AND f.requester_id=p.user_id)))) AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=p.user_id) OR (b.blocker_id=p.user_id AND b.blocked_id=?)) ORDER BY mine.created_at DESC LIMIT 100`, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Postingan tersimpan belum dapat dimuat.")
		return
	}
	defer rows.Close()
	out := []postView{}
	for rows.Next() {
		var p postView
		var image, avatar sql.NullString
		var u publicUser
		var heart, laugh, care, wow, sad int
		if err := rows.Scan(&p.ID, &p.Caption, &image, &p.CreatedAt, &u.ID, &u.Name, &u.Username, &u.Bio, &avatar, &p.LikeCount, &p.LikedByMe, &p.ReplyCount, &p.SavedByMe, &p.MyReaction, &heart, &laugh, &care, &wow, &sad); err != nil {
			fail(w, 500, "Postingan tersimpan belum dapat dimuat.")
			return
		}
		p.Image = image.String
		u.Avatar = avatar.String
		p.Author = u
		p.Reactions = map[string]int{"heart": heart, "laugh": laugh, "care": care, "wow": wow, "sad": sad}
		out = append(out, p)
	}
	writeJSON(w, 200, out)
}

func (s *server) savePost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	id := r.PathValue("id")
	if !s.canViewPost(me.ID, id) {
		fail(w, 404, "Postingan tidak ditemukan.")
		return
	}
	var err error
	if r.Method == http.MethodPut {
		_, err = s.db.Exec(`INSERT IGNORE INTO saved_posts(post_id,user_id) VALUES(?,?)`, id, me.ID)
	} else {
		_, err = s.db.Exec(`DELETE FROM saved_posts WHERE post_id=? AND user_id=?`, id, me.ID)
	}
	if err != nil {
		fail(w, 500, "Status simpan belum dapat diperbarui.")
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": r.Method == http.MethodPut})
}

func (s *server) reactPost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	id := r.PathValue("id")
	if !s.canViewPost(me.ID, id) {
		fail(w, 404, "Postingan tidak ditemukan.")
		return
	}
	var err error
	if r.Method == http.MethodPut {
		var in struct {
			Reaction string `json:"reaction"`
		}
		if decode(r, &in) != nil {
			fail(w, 400, "Reaksi tidak valid.")
			return
		}
		allowed := map[string]bool{"heart": true, "laugh": true, "care": true, "wow": true, "sad": true}
		if !allowed[in.Reaction] {
			fail(w, 400, "Pilih reaksi yang tersedia.")
			return
		}
		_, err = s.db.Exec(`INSERT INTO post_reactions(post_id,user_id,reaction) VALUES(?,?,?) ON DUPLICATE KEY UPDATE reaction=VALUES(reaction)`, id, me.ID, in.Reaction)
	} else {
		_, err = s.db.Exec(`DELETE FROM post_reactions WHERE post_id=? AND user_id=?`, id, me.ID)
	}
	if err != nil {
		fail(w, 500, "Reaksi belum dapat disimpan.")
		return
	}
	var mine string
	_ = s.db.QueryRow(`SELECT reaction FROM post_reactions WHERE post_id=? AND user_id=?`, id, me.ID).Scan(&mine)
	counts := map[string]int{"heart": 0, "laugh": 0, "care": 0, "wow": 0, "sad": 0}
	rows, e := s.db.Query(`SELECT reaction,COUNT(*) FROM post_reactions WHERE post_id=? GROUP BY reaction`, id)
	if e == nil {
		defer rows.Close()
		for rows.Next() {
			var key string
			var n int
			if rows.Scan(&key, &n) == nil {
				counts[key] = n
			}
		}
	}
	writeJSON(w, 200, map[string]any{"myReaction": mine, "reactions": counts})
}

func (s *server) stories(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	_, _ = s.db.Exec(`DELETE FROM stories WHERE expires_at<=CURRENT_TIMESTAMP(6) LIMIT 100`)
	rows, err := s.db.Query(`SELECT s.id,s.caption,s.image,s.created_at,s.expires_at,(s.user_id=? OR EXISTS(SELECT 1 FROM story_views sv WHERE sv.story_id=s.id AND sv.user_id=?)),u.id,u.name,u.username,u.bio,u.avatar FROM stories s JOIN users u ON u.id=s.user_id WHERE s.expires_at>CURRENT_TIMESTAMP(6) AND (s.user_id=? OR EXISTS(SELECT 1 FROM friendships f WHERE f.state='friends' AND ((f.requester_id=? AND f.recipient_id=s.user_id) OR (f.recipient_id=? AND f.requester_id=s.user_id)))) AND NOT EXISTS(SELECT 1 FROM blocked_users b WHERE (b.blocker_id=? AND b.blocked_id=s.user_id) OR (b.blocker_id=s.user_id AND b.blocked_id=?)) ORDER BY s.created_at`, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Cerita belum dapat dimuat.")
		return
	}
	defer rows.Close()
	out := []storyView{}
	for rows.Next() {
		var st storyView
		var image, avatar sql.NullString
		if rows.Scan(&st.ID, &st.Caption, &image, &st.CreatedAt, &st.ExpiresAt, &st.Viewed, &st.Author.ID, &st.Author.Name, &st.Author.Username, &st.Author.Bio, &avatar) != nil {
			continue
		}
		st.Image = image.String
		st.Author.Avatar = avatar.String
		out = append(out, st)
	}
	writeJSON(w, 200, out)
}

func (s *server) canViewStory(userID, storyID string) bool {
	var owner string
	if s.db.QueryRow(`SELECT user_id FROM stories WHERE id=? AND expires_at>CURRENT_TIMESTAMP(6)`, storyID).Scan(&owner) != nil {
		return false
	}
	return owner == userID || (s.isFriend(userID, owner) && !s.isBlocked(userID, owner))
}

func (s *server) createStory(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	if !me.Onboarding {
		fail(w, 403, "Lengkapi profil terlebih dahulu.")
		return
	}
	var in struct {
		Caption string `json:"caption"`
		Image   string `json:"image"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Cerita tidak valid.")
		return
	}
	in.Caption = strings.TrimSpace(in.Caption)
	if len([]rune(in.Caption)) > 280 || (in.Caption == "" && in.Image == "") {
		fail(w, 400, "Tambahkan teks atau foto. Teks maksimal 280 karakter.")
		return
	}
	if in.Image != "" {
		parts := strings.SplitN(in.Image, ",", 2)
		if len(parts) != 2 || !(strings.HasPrefix(parts[0], "data:image/jpeg;base64") || strings.HasPrefix(parts[0], "data:image/png;base64") || strings.HasPrefix(parts[0], "data:image/webp;base64")) {
			fail(w, 400, "Gunakan foto JPEG, PNG, atau WebP.")
			return
		}
		raw, e := base64.StdEncoding.DecodeString(parts[1])
		if e != nil || len(raw) > 3<<20 {
			fail(w, 400, "Ukuran foto maksimal 3 MB.")
			return
		}
	}
	id := randomID()
	_, err := s.db.Exec(`INSERT INTO stories(id,user_id,caption,image,expires_at) VALUES(?,?,?,?,DATE_ADD(CURRENT_TIMESTAMP(6),INTERVAL 24 HOUR))`, id, me.ID, in.Caption, nullString(in.Image))
	if err != nil {
		fail(w, 500, "Cerita belum dapat disimpan.")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id})
}

func (s *server) viewStory(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	id := r.PathValue("id")
	if !s.canViewStory(me.ID, id) {
		fail(w, 404, "Cerita tidak ditemukan.")
		return
	}
	if _, err := s.db.Exec(`INSERT IGNORE INTO story_views(story_id,user_id) VALUES(?,?)`, id, me.ID); err != nil {
		fail(w, 500, "Tampilan cerita belum dapat disimpan.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) deleteStory(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	res, err := s.db.Exec(`DELETE FROM stories WHERE id=? AND user_id=?`, r.PathValue("id"), me.ID)
	if err != nil {
		fail(w, 500, "Cerita belum dapat dihapus.")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "Cerita tidak ditemukan.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) likePost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	postID := r.PathValue("id")
	if !s.canViewPost(me.ID, postID) {
		fail(w, 404, "Postingan tidak ditemukan.")
		return
	}
	var err error
	if r.Method == http.MethodPut {
		var result sql.Result
		result, err = s.db.Exec(`INSERT IGNORE INTO post_likes(post_id,user_id) VALUES(?,?)`, postID, me.ID)
		if err == nil {
			if inserted, _ := result.RowsAffected(); inserted > 0 {
				var ownerID string
				if s.db.QueryRow(`SELECT user_id FROM posts WHERE id=?`, postID).Scan(&ownerID) == nil {
					s.addNotification(ownerID, me.ID, "like", postID)
				}
			}
		}
	} else {
		_, err = s.db.Exec(`DELETE FROM post_likes WHERE post_id=? AND user_id=?`, postID, me.ID)
	}
	if err != nil {
		fail(w, 500, "Like belum dapat disimpan.")
		return
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM post_likes WHERE post_id=?`, postID).Scan(&count); err != nil {
		fail(w, 500, "Jumlah like belum dapat dimuat.")
		return
	}
	writeJSON(w, 200, map[string]any{"liked": r.Method == http.MethodPut, "likeCount": count})
}

func (s *server) postReplies(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	postID := r.PathValue("id")
	if !s.canViewPost(me.ID, postID) {
		fail(w, 404, "Postingan tidak ditemukan.")
		return
	}
	rows, err := s.db.Query(`SELECT pr.id,pr.body,pr.created_at,u.id,u.name,u.username,u.bio,u.avatar FROM (SELECT id,body,created_at,user_id FROM post_replies WHERE post_id=? ORDER BY created_at DESC LIMIT 50) pr JOIN users u ON u.id=pr.user_id ORDER BY pr.created_at ASC`, postID)
	if err != nil {
		fail(w, 500, "Balasan belum dapat dimuat.")
		return
	}
	defer rows.Close()
	out := []postReplyView{}
	for rows.Next() {
		var reply postReplyView
		var avatar sql.NullString
		if err := rows.Scan(&reply.ID, &reply.Body, &reply.CreatedAt, &reply.Author.ID, &reply.Author.Name, &reply.Author.Username, &reply.Author.Bio, &avatar); err != nil {
			fail(w, 500, "Balasan belum dapat dimuat.")
			return
		}
		reply.Author.Avatar = avatar.String
		out = append(out, reply)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "Balasan belum dapat dimuat.")
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) createPostReply(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	postID := r.PathValue("id")
	if !s.canViewPost(me.ID, postID) {
		fail(w, 404, "Postingan tidak ditemukan.")
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Balasan tidak valid.")
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" || len([]rune(in.Body)) > 1000 {
		fail(w, 400, "Tulis balasan maksimal 1000 karakter.")
		return
	}
	id := randomID()
	if _, err := s.db.Exec(`INSERT INTO post_replies(id,post_id,user_id,body) VALUES(?,?,?,?)`, id, postID, me.ID, in.Body); err != nil {
		fail(w, 500, "Balasan belum dapat disimpan.")
		return
	}
	var ownerID string
	if s.db.QueryRow(`SELECT user_id FROM posts WHERE id=?`, postID).Scan(&ownerID) == nil {
		s.addNotification(ownerID, me.ID, "reply", postID)
	}
	writeJSON(w, 201, postReplyView{ID: id, Body: in.Body, CreatedAt: time.Now(), Author: safe(me)})
}

func (s *server) updatePost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	var in struct {
		Caption *string `json:"caption"`
		Image   *string `json:"image"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Data postingan tidak valid.")
		return
	}
	if in.Caption == nil {
		fail(w, 400, "Isi cerita tidak valid.")
		return
	}
	caption := strings.TrimSpace(*in.Caption)
	if len([]rune(caption)) > 1000 {
		fail(w, 400, "Cerita maksimal 1000 karakter.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Postingan belum dapat diperbarui.")
		return
	}
	defer tx.Rollback()
	var oldImage sql.NullString
	var canEdit bool
	err = tx.QueryRow(`SELECT image,created_at>=NOW()-INTERVAL 30 MINUTE FROM posts WHERE id=? AND user_id=? FOR UPDATE`, r.PathValue("id"), me.ID).Scan(&oldImage, &canEdit)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "Postingan tidak ditemukan atau bukan milikmu.")
		return
	}
	if err != nil {
		fail(w, 500, "Postingan belum dapat dimuat.")
		return
	}
	if !canEdit {
		fail(w, 403, "Postingan hanya bisa diedit dalam 30 menit setelah dibagikan.")
		return
	}
	image := oldImage.String
	if in.Image != nil {
		image = *in.Image
		if image != "" {
			parts := strings.SplitN(image, ",", 2)
			if len(parts) != 2 || !(strings.HasPrefix(parts[0], "data:image/jpeg;base64") || strings.HasPrefix(parts[0], "data:image/png;base64") || strings.HasPrefix(parts[0], "data:image/webp;base64")) {
				fail(w, 400, "Gunakan foto JPEG, PNG, atau WebP.")
				return
			}
			raw, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil || len(raw) > 3<<20 {
				fail(w, 400, "Ukuran foto maksimal 3 MB.")
				return
			}
		}
	}
	if caption == "" && image == "" {
		fail(w, 400, "Tambahkan cerita atau foto.")
		return
	}
	if _, err = tx.Exec(`UPDATE posts SET caption=?,image=? WHERE id=? AND user_id=?`, caption, nullString(image), r.PathValue("id"), me.ID); err != nil {
		fail(w, 500, "Postingan belum dapat diperbarui.")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "Postingan belum dapat diperbarui.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) deletePost(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	res, err := s.db.Exec(`DELETE FROM posts WHERE id=? AND user_id=?`, r.PathValue("id"), me.ID)
	if err != nil {
		fail(w, 500, "Postingan belum dapat dihapus.")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "Postingan tidak ditemukan atau bukan milikmu.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type conversation struct {
	User        publicUser `json:"user"`
	LastMessage string     `json:"lastMessage"`
	LastAt      time.Time  `json:"lastAt"`
	Unread      int        `json:"unread"`
}

func (s *server) conversations(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	rows, err := s.db.Query(`SELECT u.id,u.name,u.username,u.bio,u.avatar,COALESCE((SELECT NULLIF(m.body,'') FROM messages m WHERE (m.sender_id=? AND m.recipient_id=u.id) OR (m.sender_id=u.id AND m.recipient_id=?) ORDER BY m.created_at DESC LIMIT 1),(SELECT IF(ma.message_id IS NOT NULL,'Foto',NULL) FROM messages m LEFT JOIN message_attachments ma ON ma.message_id=m.id WHERE (m.sender_id=? AND m.recipient_id=u.id) OR (m.sender_id=u.id AND m.recipient_id=?) ORDER BY m.created_at DESC LIMIT 1),''),COALESCE((SELECT m.created_at FROM messages m WHERE (m.sender_id=? AND m.recipient_id=u.id) OR (m.sender_id=u.id AND m.recipient_id=?) ORDER BY m.created_at DESC LIMIT 1),u.created_at),(SELECT COUNT(*) FROM messages um WHERE um.sender_id=u.id AND um.recipient_id=? AND um.created_at>COALESCE((SELECT cr.last_read_at FROM conversation_reads cr WHERE cr.user_id=? AND cr.peer_id=u.id),'1000-01-01 00:00:00')) FROM friendships f JOIN users u ON u.id=IF(f.requester_id=?,f.recipient_id,f.requester_id) WHERE (f.requester_id=? OR f.recipient_id=?) AND f.state='friends'`, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID, me.ID)
	if err != nil {
		fail(w, 500, "Daftar percakapan gagal dimuat.")
		return
	}
	defer rows.Close()
	out := []conversation{}
	for rows.Next() {
		var c conversation
		var avatar sql.NullString
		if rows.Scan(&c.User.ID, &c.User.Name, &c.User.Username, &c.User.Bio, &avatar, &c.LastMessage, &c.LastAt, &c.Unread) == nil {
			c.User.Avatar = avatar.String
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	writeJSON(w, 200, out)
}
func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	other := r.URL.Query().Get("userId")
	if !s.isFriend(me.ID, other) {
		fail(w, 403, "Chat hanya tersedia untuk teman.")
		return
	}
	if _, err := s.db.Exec(`INSERT INTO conversation_reads(user_id,peer_id,last_read_at) VALUES(?,?,CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE last_read_at=GREATEST(last_read_at,CURRENT_TIMESTAMP(6))`, me.ID, other); err != nil {
		fail(w, 500, "Status pesan gagal diperbarui.")
		return
	}
	_, _ = s.db.Exec(`UPDATE notifications SET read_at=COALESCE(read_at,CURRENT_TIMESTAMP(6)) WHERE user_id=? AND actor_id=? AND kind='message' AND read_at IS NULL`, me.ID, other)
	rows, err := s.db.Query(`SELECT m.id,m.sender_id,m.recipient_id,m.body,m.created_at,COALESCE(a.image,''),CASE WHEN m.sender_id=? THEN IF(cr.last_read_at>=m.created_at,1,0) ELSE 0 END FROM messages m LEFT JOIN message_attachments a ON a.message_id=m.id LEFT JOIN conversation_reads cr ON cr.user_id=m.recipient_id AND cr.peer_id=m.sender_id WHERE (m.sender_id=? AND m.recipient_id=?) OR (m.sender_id=? AND m.recipient_id=?) ORDER BY m.created_at DESC LIMIT 100`, other, me.ID, other, other, me.ID)
	if err != nil {
		fail(w, 500, "Pesan gagal dimuat.")
		return
	}
	defer rows.Close()
	type message struct {
		ID          string    `json:"id"`
		SenderID    string    `json:"senderId"`
		RecipientID string    `json:"recipientId"`
		Body        string    `json:"body"`
		Image       string    `json:"image,omitempty"`
		CreatedAt   time.Time `json:"createdAt"`
		Read        bool      `json:"read"`
	}
	out := []message{}
	for rows.Next() {
		var m message
		if rows.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Body, &m.CreatedAt, &m.Image, &m.Read) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	writeJSON(w, 200, out)
}
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	me, ok := s.current(r)
	if !ok {
		fail(w, 401, "Silakan masuk terlebih dahulu.")
		return
	}
	var in struct {
		UserID string `json:"userId"`
		Body   string `json:"body"`
		Image  string `json:"image"`
	}
	if decode(r, &in) != nil {
		fail(w, 400, "Pesan tidak valid.")
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if len([]rune(in.Body)) > 4000 || (in.Body == "" && in.Image == "") {
		fail(w, 400, "Tulis pesan atau pilih foto.")
		return
	}
	if in.Image != "" {
		parts := strings.SplitN(in.Image, ",", 2)
		if len(parts) != 2 || !(strings.HasPrefix(parts[0], "data:image/jpeg;base64") || strings.HasPrefix(parts[0], "data:image/png;base64") || strings.HasPrefix(parts[0], "data:image/webp;base64")) {
			fail(w, 400, "Format foto chat harus JPG, PNG, atau WebP.")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil || len(raw) > 3<<20 {
			fail(w, 400, "Ukuran foto chat maksimal 3 MB.")
			return
		}
	}
	if !s.isFriend(me.ID, in.UserID) {
		fail(w, 403, "Chat hanya tersedia untuk teman.")
		return
	}
	id := randomID()
	tx, err := s.db.Begin()
	if err != nil {
		fail(w, 500, "Pesan belum terkirim.")
		return
	}
	if _, err = tx.Exec(`INSERT INTO messages(id,sender_id,recipient_id,body) VALUES(?,?,?,?)`, id, me.ID, in.UserID, in.Body); err == nil && in.Image != "" {
		_, err = tx.Exec(`INSERT INTO message_attachments(message_id,image) VALUES(?,?)`, id, in.Image)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		fail(w, 500, "Pesan belum terkirim.")
		return
	}
	s.addNotification(in.UserID, me.ID, "message", id)
	writeJSON(w, 201, map[string]any{"id": id, "senderId": me.ID, "recipientId": in.UserID, "body": in.Body, "image": in.Image, "createdAt": time.Now(), "read": false})
}
