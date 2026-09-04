package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"

	"velib-agent/internal/agent"
	"velib-agent/internal/observability"
)

// Le flux SSE : la réponse arrive mot à mot, pas d'un bloc à la fin.
//
// Server-Sent Events plutôt que WebSocket : le flux est unidirectionnel, SSE
// est du simple HTTP, se reconnecte tout seul côté navigateur, et traverse les
// proxys sans négociation d'upgrade. Un WebSocket aurait ajouté une machine à
// états bidirectionnelle pour un besoin qui ne l'est pas.

type sendMessageRequest struct {
	Message string `json:"message"`
}

// sseEvent est l'enveloppe unique du flux. Un seul type d'événement porteur
// d'un champ « type » vaut mieux que cinq types d'événements SSE nommés : le
// front n'a qu'un seul point de branchement.
type sseEvent struct {
	Type string `json:"type"`

	// token : un fragment de la réponse.
	Content string `json:"content,omitempty"`

	// tool : l'agent appelle un outil. Envoyé pour que l'utilisateur voie ce
	// qui se passe pendant l'attente — un agent qui réfléchit en silence
	// pendant quatre secondes passe pour un agent bloqué.
	Tool string `json:"tool,omitempty"`

	// error : message destiné à l'utilisateur.
	Error string `json:"error,omitempty"`

	// done : la réponse complète, pour que le front n'ait pas à recoller les
	// fragments lui-même, et de quoi afficher le coût du tour.
	Full   string `json:"full,omitempty"`
	Tokens *usage `json:"tokens,omitempty"`
}

type usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	convID := r.PathValue("id")

	var req sendMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corps de requête invalide",
			`attendu : {"message": "votre question"}`)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message vide", "")
		return
	}

	// La conversation doit exister. On refuse de créer implicitement : un
	// identifiant inconnu est plus souvent un bug côté client qu'une intention.
	sess, err := s.agent.Sessions.GetSession(r.Context(), agent.SessionKey(uid, convID))
	if err != nil || sess == nil {
		writeError(w, http.StatusNotFound, "conversation introuvable",
			"créer la conversation avec POST /api/conversations")
		return
	}

	// Le titre est posé au premier message, avant tout appel au modèle. Si la
	// génération échoue ensuite, la conversation reste identifiable dans la
	// liste au lieu de rester « Nouvelle conversation ».
	if len(sess.Events) == 0 {
		s.setTitle(r, uid, convID, deriveTitle(req.Message))
	}

	s.streamAnswer(w, r, uid, convID, req.Message)
}

// setTitle écrit le titre dans l'état de session.
//
// Un échec ici n'interrompt PAS la conversation : le titre est un confort
// d'affichage, pas une donnée métier. Faire échouer une réponse parce qu'un
// libellé n'a pas pu s'écrire serait disproportionné.
func (s *Server) setTitle(r *http.Request, uid, convID, title string) {
	err := s.agent.Sessions.UpdateSessionState(
		r.Context(),
		agent.SessionKey(uid, convID),
		session.StateMap{titleStateKey: []byte(title)},
	)
	if err != nil {
		s.log.Warn("titre non enregistré, sans conséquence sur la conversation",
			"erreur", err, "conversation", convID)
	}
}

// streamAnswer exécute le tour et pousse le flux.
func (s *Server) streamAnswer(w http.ResponseWriter, r *http.Request, uid, convID, question string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError,
			"le streaming n'est pas supporté par ce serveur", "")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Neutralise la mise en tampon de nginx : sans cet en-tête, le proxy
	// accumule la réponse et la livre d'un bloc, ce qui annule tout l'intérêt
	// du streaming sans qu'aucune erreur n'apparaisse nulle part.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(ev sseEvent) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	start := time.Now()
	events, err := s.agent.Runner.Run(
		r.Context(),
		uid,
		convID,
		model.NewUserMessage(question),
	)
	if err != nil {
		s.log.Error("démarrage du tour", "erreur", err, "conversation", convID)
		s.metrics.RecordTurn(observability.TurnRecord{
			At: start, ConversationID: convID, Status: "error",
			DurationMs: time.Since(start).Milliseconds(),
		})
		send(sseEvent{Type: "error",
			Error: "impossible de contacter le modèle, réessayer dans un instant"})
		return
	}

	var full strings.Builder
	var toolCalls []string
	var tokens *usage
	// dataStale retient si un outil a servi une donnée périmée pendant ce tour.
	// Sans ça, le champ du même nom valait toujours false dans le tracker, y
	// compris pendant une panne de la source — exactement le cas qu'il existe
	// pour rendre visible.
	dataStale := false

	for ev := range events {
		// Le client a fermé l'onglet. On sort de la boucle sans paniquer : le
		// Runner, lui, finit son tour et persiste ce qu'il a produit, donc la
		// réponse sera là au rechargement.
		select {
		case <-r.Context().Done():
			s.log.Info("client déconnecté en cours de réponse",
				"conversation", convID, "recu", full.Len())
			return
		default:
		}

		if ev.Response == nil {
			continue
		}

		// Erreur remontée par le framework.
		if ev.IsError() {
			// Le detail vit dans Response.Error, pas dans Object qui ne porte
			// que le type d'evenement. Logger Object seul produisait une ligne
			// « objet="" » : une erreur signalee mais impossible a diagnostiquer.
			errType, errMsg, errCode := "", "", ""
			if e := ev.Response.Error; e != nil {
				errType, errMsg = e.Type, e.Message
				if e.Code != nil {
					errCode = *e.Code
				}
			}
			s.log.Error("erreur pendant le tour", "conversation", convID,
				"type", errType, "message", errMsg, "code", errCode)
			s.metrics.RecordTurn(observability.TurnRecord{
				At: start, ConversationID: convID, Status: "error",
				DurationMs: time.Since(start).Milliseconds(), Tools: toolCalls,
			})
			send(sseEvent{Type: "error", Error: messageUtilisateur(errMsg)})
			return
		}

		if ev.Response.Usage != nil {
			tokens = &usage{
				Prompt:     ev.Response.Usage.PromptTokens,
				Completion: ev.Response.Usage.CompletionTokens,
				Total:      ev.Response.Usage.TotalTokens,
			}
		}

		for _, ch := range ev.Response.Choices {
			// Appels d'outils : on les annonce pour rendre l'attente lisible.
			for _, tc := range ch.Message.ToolCalls {
				if tc.Function.Name == "" {
					continue
				}
				toolCalls = append(toolCalls, tc.Function.Name)
				send(sseEvent{Type: "tool", Tool: tc.Function.Name})
			}

			// Un message d'outil qui porte "stale":true signale que le cache a
			// servi une donnée datée faute de source joignable.
			if ch.Message.ToolID != "" && strings.Contains(ch.Message.Content, `"stale":true`) {
				dataStale = true
			}

			// Fragment de texte.
			if d := ch.Delta.Content; d != "" {
				full.WriteString(d)
				send(sseEvent{Type: "token", Content: d})
			}
		}
	}

	answer := full.String()

	// Le tour est enregistré AVANT tout autre traitement de fin : si quelque
	// chose échoue après, la mesure existe quand même. Persister avant
	// d'enrichir, ici appliqué à la métrologie.
	rec := observability.TurnRecord{
		At:             start,
		ConversationID: convID,
		DurationMs:     time.Since(start).Milliseconds(),
		Tools:          toolCalls,
		AnswerChars:    len(answer),
		Status:         "ok",
		DataStale:      dataStale,
	}
	if strings.TrimSpace(answer) == "" {
		rec.Status = "empty"
	}
	if tokens != nil {
		rec.PromptTokens = tokens.Prompt
		rec.CompletionTokens = tokens.Completion
	}
	s.metrics.RecordTurn(rec)

	s.log.Info("tour terminé",
		"conversation", convID,
		"duree_ms", time.Since(start).Milliseconds(),
		"outils", toolCalls,
		"caracteres", len(answer))

	// Un tour qui se termine sans texte n'est pas un succès : sans ce cas, le
	// front afficherait une bulle vide et l'utilisateur croirait à un bug de
	// son navigateur.
	if strings.TrimSpace(answer) == "" {
		send(sseEvent{Type: "error",
			Error: "le modèle n'a produit aucune réponse, reformuler la question"})
		return
	}

	send(sseEvent{Type: "done", Full: answer, Tokens: tokens})
}

// handleHealth rend l'état du service.
//
// On teste PostgreSQL par un appel réel plutôt que de renvoyer « ok » en dur :
// une sonde qui répond toujours vrai ne sert à rien, et c'est précisément ce
// genre de contrôle vert en permanence qui laisse une panne passer inaperçue.
// messageUtilisateur traduit l'erreur du fournisseur en une phrase actionnable.
//
// Toutes les pannes ne se valent pas. Une limite de debit est PASSAGERE : la
// bonne reaction est d'attendre et de reessayer, et l'utilisateur ne peut pas
// le deviner derriere « une erreur est survenue ». Un defaut d'authentification
// est DEFINITIF sans intervention : lui dire de reessayer le ferait tourner en
// rond.
//
// On ne recopie jamais le message brut du fournisseur : il contient l'URL, le
// nom de l'organisation et parfois des fragments de configuration. Le detail
// complet reste dans le journal, cote serveur.
func messageUtilisateur(brut string) string {
	b := strings.ToLower(brut)
	switch {
	case strings.Contains(b, "429"), strings.Contains(b, "rate limit"):
		return "Le quota du fournisseur de modèle est atteint. Réessayez dans une minute."
	case strings.Contains(b, "401"), strings.Contains(b, "invalid_api_key"):
		return "La clé d'API du modèle est refusée. Vérifiez OPENAI_API_KEY."
	case strings.Contains(b, "context deadline exceeded"), strings.Contains(b, "timeout"):
		return "Le modèle n'a pas répondu à temps. Réessayez."
	default:
		return "Une erreur est survenue pendant la génération de la réponse."
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := map[string]any{
		"status":   "ok",
		"model":    s.cfg.ModelName,
		"postgres": "ok",
	}
	code := http.StatusOK

	if err := s.agent.Ping(ctx); err != nil {
		status["status"] = "degraded"
		status["postgres"] = err.Error()
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}
