package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"

	"velib-agent/internal/agent"
)

// Le cycle de vie des conversations : créer, lister, rouvrir, supprimer.
//
// Tout s'appuie sur le service de sessions PostgreSQL du framework. Rien n'est
// stocké en mémoire dans ce processus : arrêter la pile et la relancer ne perd
// rien, ce que la spécification exige explicitement.

// titleStateKey porte le titre lisible d'une conversation dans l'état de session.
//
// L'état de session est le bon endroit : il est persisté avec la conversation et
// suit sa suppression. Une table maison à côté aurait introduit un deuxième
// cycle de vie à garder synchronisé, donc une occasion de laisser des orphelins.
const titleStateKey = "title"

// conversationSummary est la vue « liste ».
type conversationSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// messageView est un message tel que le front l'affiche.
type messageView struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

type conversationDetail struct {
	conversationSummary
	Messages []messageView `json:"messages"`
}

// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	id := uuid.NewString()

	sess, err := s.agent.Sessions.CreateSession(
		r.Context(),
		agent.SessionKey(uid, id),
		session.StateMap{titleStateKey: []byte("Nouvelle conversation")},
	)
	if err != nil {
		s.log.Error("création de conversation", "erreur", err)
		writeError(w, http.StatusInternalServerError,
			"impossible de créer la conversation",
			"vérifier que PostgreSQL est joignable")
		return
	}

	writeJSON(w, http.StatusCreated, conversationSummary{
		ID:        sess.ID,
		Title:     "Nouvelle conversation",
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
	})
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)

	// WithListSessionOnlyMeta évite de rapatrier tous les événements de toutes
	// les conversations pour n'afficher qu'une liste de titres. Sur une
	// conversation longue, la différence n'est pas cosmétique.
	sessions, err := s.agent.Sessions.ListSessions(
		r.Context(),
		agent.UserKey(uid),
		session.WithListSessionOnlyMeta(),
	)
	if err != nil {
		s.log.Error("liste des conversations", "erreur", err)
		writeError(w, http.StatusInternalServerError,
			"impossible de lister les conversations",
			"vérifier que PostgreSQL est joignable")
		return
	}

	out := make([]conversationSummary, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, conversationSummary{
			ID:        sess.ID,
			Title:     titleOf(sess),
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
		})
	}

	// La plus récemment utilisée en tête : c'est celle qu'on veut rouvrir.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})

	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	id := r.PathValue("id")

	sess, err := s.agent.Sessions.GetSession(r.Context(), agent.SessionKey(uid, id))
	if err != nil {
		s.log.Error("lecture de conversation", "erreur", err, "id", id)
		writeError(w, http.StatusInternalServerError,
			"impossible de lire la conversation", "")
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "conversation introuvable", "")
		return
	}

	writeJSON(w, http.StatusOK, conversationDetail{
		conversationSummary: conversationSummary{
			ID:           sess.ID,
			Title:        titleOf(sess),
			MessageCount: len(sess.Events),
			CreatedAt:    sess.CreatedAt,
			UpdatedAt:    sess.UpdatedAt,
		},
		Messages: messagesOf(sess),
	})
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	id := r.PathValue("id")

	if err := s.agent.Sessions.DeleteSession(r.Context(), agent.SessionKey(uid, id)); err != nil {
		s.log.Error("suppression de conversation", "erreur", err, "id", id)
		writeError(w, http.StatusInternalServerError,
			"impossible de supprimer la conversation", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// Lecture de l'historique
// ─────────────────────────────────────────────────────────────────────────────

// messagesOf reconstruit l'historique lisible depuis les événements de session.
//
// Le flux d'événements contient beaucoup plus que la conversation : appels
// d'outils, fragments de streaming, marqueurs internes. Le front n'affiche que
// ce qu'un humain a écrit ou lu, donc on filtre.
//
// ⚠️ En streaming, un même message d'assistant arrive en dizaines d'événements
// partiels. Les recopier tels quels afficherait la réponse lettre par lettre
// sur des lignes séparées. On ne garde donc que les messages complets, et on
// fusionne les fragments consécutifs du même auteur en dernier recours.
func messagesOf(sess *session.Session) []messageView {
	out := make([]messageView, 0, len(sess.Events))

	for _, ev := range sess.Events {
		if ev.Response == nil || len(ev.Response.Choices) == 0 {
			continue
		}
		for _, ch := range ev.Response.Choices {
			msg := ch.Message
			// Un fragment de streaming porte son texte dans Delta, pas dans
			// Message : on l'ignore, la version complète arrive en fin de tour.
			if msg.Content == "" {
				continue
			}
			role := string(msg.Role)
			if role != string(model.RoleUser) && role != string(model.RoleAssistant) {
				continue // messages d'outil : utiles au modèle, pas à l'utilisateur
			}
			out = append(out, messageView{
				Role:    role,
				Content: msg.Content,
				At:      ev.Timestamp,
			})
		}
	}
	return dedupeConsecutive(out)
}

// dedupeConsecutive fusionne les doublons successifs du même auteur.
//
// Selon la façon dont le tour a été persisté, un message d'assistant peut
// apparaître deux fois : une version partielle puis la version finale. On garde
// la plus longue, qui est la complète.
func dedupeConsecutive(in []messageView) []messageView {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, m := range in[1:] {
		last := &out[len(out)-1]
		if last.Role == m.Role && strings.HasPrefix(m.Content, last.Content) {
			last.Content = m.Content
			last.At = m.At
			continue
		}
		out = append(out, m)
	}
	return out
}

func titleOf(sess *session.Session) string {
	if raw, ok := sess.State[titleStateKey]; ok && len(raw) > 0 {
		return strings.Trim(string(raw), `"`)
	}
	return "Conversation"
}

// deriveTitle fabrique un titre depuis la première question.
//
// Une liste de « Nouvelle conversation » est inutilisable dès la troisième
// conversation. Le premier message est le meilleur titre disponible, et il ne
// coûte aucun appel de modèle : demander à un LLM de résumer une question de
// dix mots serait payer pour ce qu'une troncature fait aussi bien.
func deriveTitle(question string) string {
	t := strings.Join(strings.Fields(question), " ")
	const max = 60
	if len(t) <= max {
		return t
	}
	// Coupe sur le dernier espace pour ne pas trancher un mot en deux.
	cut := strings.LastIndex(t[:max], " ")
	if cut < 20 {
		cut = max
	}
	return t[:cut] + "…"
}
