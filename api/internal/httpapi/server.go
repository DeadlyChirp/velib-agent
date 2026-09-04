// Package httpapi expose l'agent et la gestion des conversations en HTTP.
//
// Deux surfaces distinctes :
//   - une API REST classique pour le cycle de vie des conversations ;
//   - un flux SSE pour la réponse de l'agent, mot à mot.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"velib-agent/internal/agent"
	"velib-agent/internal/config"
	"velib-agent/internal/observability"
)

// Server porte les dépendances des handlers.
type Server struct {
	agent   *agent.Service
	cfg     config.Config
	log     *slog.Logger
	metrics *observability.Metrics
}

// New construit le serveur.
func New(a *agent.Service, cfg config.Config, log *slog.Logger, m *observability.Metrics) *Server {
	return &Server{agent: a, cfg: cfg, log: log, metrics: m}
}

// Routes construit le routeur.
//
// Le routeur de la bibliothèque standard suffit : depuis Go 1.22 il gère les
// méthodes et les variables de chemin. Ajouter un framework pour six routes
// serait une dépendance à maintenir sans contrepartie.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	mux.HandleFunc("POST /api/conversations", s.handleCreateConversation)
	mux.HandleFunc("GET /api/conversations/{id}", s.handleGetConversation)
	mux.HandleFunc("DELETE /api/conversations/{id}", s.handleDeleteConversation)
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.handleSendMessage)

	return s.withCORS(s.withLogging(mux))
}

// ─────────────────────────────────────────────────────────────────────────────
// Identité de l'utilisateur
// ─────────────────────────────────────────────────────────────────────────────

// defaultUserID est l'utilisateur retenu quand aucun en-tête n'est fourni.
//
// Choix assumé et documenté : la spécification ne demande pas d'authentification, et en
// inventer une aurait été du périmètre en plus au détriment du reste. Le modèle
// de données du framework est en revanche déjà multi-utilisateurs — les clés de
// session portent un userID — donc brancher une vraie authentification plus
// tard ne demanderait que de remplir cet en-tête depuis un jeton vérifié.
const defaultUserID = "demo"

func userID(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-User-ID")); v != "" {
		return v
	}
	return defaultUserID
}

// ─────────────────────────────────────────────────────────────────────────────
// Intergiciels
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else if len(s.cfg.CORSOrigins) == 1 && s.cfg.CORSOrigins[0] == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID")
		w.Header().Set("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, o := range s.cfg.CORSOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// statusRecorder capture le code de retour pour le journal.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush propage le vidage du tampon. Sans cette méthode, l'enveloppe casse le
// streaming SSE en silence : le handler appelle Flush, l'assertion de type
// échoue, et la réponse n'arrive qu'à la fin. C'est exactement le genre de
// panne muette qu'on ne voit qu'en testant vraiment le flux.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		s.log.Info("requête",
			"methode", r.Method,
			"chemin", r.URL.Path,
			"statut", rec.status,
			"duree_ms", time.Since(start).Milliseconds())
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Réponses
// ─────────────────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiError est la forme unique des erreurs. Un front qui n'a qu'une seule
// structure à gérer affiche des messages utiles au lieu de « une erreur est
// survenue ».
type apiError struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg, hint string) {
	writeJSON(w, status, apiError{Error: msg, Hint: hint})
}
