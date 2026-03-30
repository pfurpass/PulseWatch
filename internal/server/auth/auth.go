// Package auth implementiert einfache Multi-User-Authentifizierung
// mit HMAC-signierten Session-Tokens, Rollen und einem JSON-User-Store.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Role definiert Berechtigungslevel
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// User ist ein registrierter Benutzer
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"` // SHA-256 hex
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

// Session ist eine aktive Login-Session
type Session struct {
	Token     string
	UserID    string
	Username  string
	Role      Role
	ExpiresAt time.Time
}

// Manager verwaltet Users und Sessions
type Manager struct {
	mu       sync.RWMutex
	users    map[string]*User   // id → user
	byName   map[string]*User   // username → user
	sessions map[string]*Session
	secret   []byte
	path     string
	logger   *slog.Logger
	enabled  bool
}

// New erstellt einen neuen Auth-Manager
// Wenn path leer ist, wird Auth deaktiviert (Open Access).
func New(path string, secret string) *Manager {
	m := &Manager{
		users:    make(map[string]*User),
		byName:   make(map[string]*User),
		sessions: make(map[string]*Session),
		secret:   []byte(secret),
		path:     path,
		logger:   slog.Default(),
		enabled:  path != "",
	}

	if path != "" {
		m.load()
		// Admin anlegen falls keine User existieren
		if len(m.users) == 0 {
			m.createDefaultAdmin()
		}
		// Session-Cleanup starten
		go m.cleanupLoop()
	}

	return m
}

// Enabled gibt an ob Auth aktiv ist
func (m *Manager) Enabled() bool { return m.enabled }

// ── User Management ───────────────────────────────────────────────────────

func (m *Manager) CreateUser(username, password string, role Role) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.byName[username]; exists {
		return nil, fmt.Errorf("user %q already exists", username)
	}

	user := &User{
		ID:           randHex(8),
		Username:     username,
		PasswordHash: hashPassword(password),
		Role:         role,
		CreatedAt:    time.Now(),
	}

	m.users[user.ID] = user
	m.byName[username] = user
	m.persist()
	return user, nil
}

func (m *Manager) ListUsers() []*User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*User, 0, len(m.users))
	for _, u := range m.users {
		result = append(result, u)
	}
	return result
}

func (m *Manager) DeleteUser(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return fmt.Errorf("user not found")
	}
	delete(m.users, id)
	delete(m.byName, u.Username)
	m.persist()
	return nil
}

func (m *Manager) ChangePassword(id, newPassword string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return fmt.Errorf("user not found")
	}
	u.PasswordHash = hashPassword(newPassword)
	m.persist()
	return nil
}

// ── Login / Logout ────────────────────────────────────────────────────────

// Login prüft Credentials und gibt ein Token zurück
func (m *Manager) Login(username, password string) (string, error) {
	m.mu.RLock()
	u, ok := m.byName[username]
	m.mu.RUnlock()

	if !ok || u.PasswordHash != hashPassword(password) {
		return "", fmt.Errorf("invalid credentials")
	}

	token := m.signToken(u.ID)
	session := &Session{
		Token:     token,
		UserID:    u.ID,
		Username:  u.Username,
		Role:      u.Role,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	m.mu.Lock()
	m.sessions[token] = session
	m.mu.Unlock()

	m.logger.Info("user logged in", "user", username, "role", u.Role)
	return token, nil
}

// Logout invalidiert ein Token
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

// ValidateToken prüft ein Token und gibt die Session zurück
func (m *Manager) ValidateToken(token string) (*Session, bool) {
	if !m.enabled {
		return &Session{Username: "anonymous", Role: RoleAdmin}, true
	}
	m.mu.RLock()
	s, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok || time.Now().After(s.ExpiresAt) {
		return nil, false
	}
	return s, true
}

// ── HTTP Middleware ───────────────────────────────────────────────────────

// Middleware gibt einen HTTP-Handler zurück der Auth prüft
func (m *Manager) Middleware(minRole Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.enabled {
			next.ServeHTTP(w, r)
			return
		}

		token := extractToken(r)
		session, ok := m.ValidateToken(token)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if !hasPermission(session.Role, minRole) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		// Session in Context-Header weiterreichen
		r.Header.Set("X-User", session.Username)
		r.Header.Set("X-Role", string(session.Role))
		next.ServeHTTP(w, r)
	})
}

// HandleLogin ist der POST /api/v1/auth/login Handler
func (m *Manager) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	token, err := m.Login(body.Username, body.Password)
	if err != nil {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token":    token,
		"username": body.Username,
	})
}

// HandleLogout ist der POST /api/v1/auth/logout Handler
func (m *Manager) HandleLogout(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	m.Logout(token)
	w.WriteHeader(http.StatusNoContent)
}

// HandleMe gibt den aktuellen User zurück
func (m *Manager) HandleMe(w http.ResponseWriter, r *http.Request) {
	if !m.enabled {
		json.NewEncoder(w).Encode(map[string]string{"username": "admin", "role": "admin"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"username": r.Header.Get("X-User"),
		"role":     r.Header.Get("X-Role"),
	})
}

// HandleUsers gibt alle User zurück (nur Admin)
func (m *Manager) HandleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		users := m.ListUsers()
		// Passwort-Hashes nicht zurückgeben
		type safeUser struct {
			ID       string    `json:"id"`
			Username string    `json:"username"`
			Role     Role      `json:"role"`
			Created  time.Time `json:"created_at"`
		}
		safe := make([]safeUser, len(users))
		for i, u := range users {
			safe[i] = safeUser{u.ID, u.Username, u.Role, u.CreatedAt}
		}
		json.NewEncoder(w).Encode(map[string]any{"users": safe})

	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     Role   `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		u, err := m.CreateUser(body.Username, body.Password, body.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (m *Manager) signToken(userID string) string {
	raw := make([]byte, 16)
	rand.Read(raw)
	payload := userID + ":" + hex.EncodeToString(raw)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func hashPassword(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func extractToken(r *http.Request) string {
	// Authorization: Bearer <token>
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Cookie: pw_token=<token>
	if c, err := r.Cookie("pw_token"); err == nil {
		return c.Value
	}
	return ""
}

func hasPermission(have, need Role) bool {
	order := map[Role]int{RoleViewer: 1, RoleEditor: 2, RoleAdmin: 3}
	return order[have] >= order[need]
}

func (m *Manager) createDefaultAdmin() {
	u, err := m.CreateUser("admin", "admin", RoleAdmin)
	if err == nil {
		m.logger.Warn("created default admin user — change password immediately!",
			"username", u.Username, "password", "admin")
	}
}

func (m *Manager) load() {
	f, err := os.Open(m.path)
	if err != nil {
		return
	}
	defer f.Close()
	var users []*User
	if err := json.NewDecoder(f).Decode(&users); err != nil {
		return
	}
	for _, u := range users {
		m.users[u.ID] = u
		m.byName[u.Username] = u
	}
}

func (m *Manager) persist() {
	f, err := os.Create(m.path)
	if err != nil {
		return
	}
	defer f.Close()
	users := make([]*User, 0, len(m.users))
	for _, u := range m.users {
		users = append(users, u)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(users)
}

func (m *Manager) cleanupLoop() {
	for range time.Tick(15 * time.Minute) {
		m.mu.Lock()
		now := time.Now()
		for token, s := range m.sessions {
			if now.After(s.ExpiresAt) {
				delete(m.sessions, token)
			}
		}
		m.mu.Unlock()
	}
}
