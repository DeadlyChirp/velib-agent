// Package agent câble le Runner : modèle, outils, instruction, persistance.
//
// Ce paquet ne contient AUCUNE logique métier. Tout ce qui calcule vit dans
// internal/velib, tout ce qui expose vit dans internal/tools. Ici, on assemble.
// Cette séparation permet de tester les agrégations sans modèle et de changer
// de fournisseur de LLM sans toucher au métier.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	openaisdk "github.com/openai/openai-go"
	openaiopt "github.com/openai/openai-go/option"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessionpg "trpc.group/trpc-go/trpc-agent-go/session/postgres"

	"velib-agent/internal/config"
	"velib-agent/internal/tools"
)

// AppName identifie l'application dans les clés de session PostgreSQL.
// Le changer romprait le lien avec les conversations déjà persistées.
const AppName = "velib-agent"

// Service porte le Runner et le service de sessions.
//
// Les deux sont exposés parce que l'API HTTP en a besoin séparément : le Runner
// pour dialoguer, le service de sessions pour lister, rouvrir et supprimer des
// conversations.
type Service struct {
	Runner   runner.Runner
	Sessions session.Service
}

// New construit le service complet.
func New(cfg config.Config, reg *tools.Registry, log *slog.Logger) (*Service, error) {
	// ── Le modèle ────────────────────────────────────────────────────────────
	// Compatible OpenAI, donc interchangeable par variable d'environnement :
	// OpenAI, Mistral, DeepSeek ou un Ollama local se branchent en changeant
	// MODEL_NAME et OPENAI_BASE_URL, sans recompiler.
	modelOpts := []openai.Option{
		openai.WithAPIKey(cfg.ModelAPIKey),

		// Les modeles « raisonneurs » renvoient un champ reasoning_content que
		// le framework rejoue tel quel dans l'historique au tour suivant. Groq
		// refuse ce champ en entree et repond 400 :
		//   'messages.2' : property 'reasoning_content' is unsupported
		//
		// Concretement, le premier tour passe, l'appel d'outil part, et c'est le
		// tour d'apres qui casse — donc uniquement les conversations a plusieurs
		// echanges, celles qu'on teste en dernier.
		//
		// Le raisonnement ne sert qu'au modele pendant son tour : le retirer de
		// l'historique ne change pas les reponses. On le retire donc juste avant
		// l'envoi, ce qui laisse le projet compatible Groq sans rien casser
		// ailleurs — OpenAI et Mistral ignorent simplement un champ absent.
		openai.WithChatRequestCallback(stripReasoningContent),
	}
	if cfg.ModelBaseURL != "" {
		modelOpts = append(modelOpts, openai.WithBaseURL(cfg.ModelBaseURL))
	}
	if cfg.ReasoningEffort != "" {
		// Mesure sur gpt-oss-120b via Groq : « low » fait passer une reponse de
		// 1066 ms / 201 jetons a 587 ms / 98 jetons. Le gain vient des jetons de
		// raisonnement, invisibles dans la reponse mais factures et attendus.
		//
		// Le compromis est sans danger ICI parce que le modele ne calcule rien :
		// les agregations sont faites par les outils, en Go, de facon
		// deterministe. Il lui reste a choisir l'outil et rediger — deux taches
		// qui ne demandent pas de longues chaines de raisonnement.
		//
		// Envoye seulement si demande : ce champ n'existe pas partout.
		modelOpts = append(modelOpts, openai.WithOpenAIOptions(
			openaiopt.WithJSONSet("reasoning_effort", cfg.ReasoningEffort),
		))
	}
	llm := openai.New(cfg.ModelName, modelOpts...)

	// ── L'agent ──────────────────────────────────────────────────────────────
	registered := reg.All()
	stream := true
	ag := llmagent.New("velib",
		llmagent.WithModel(llm),
		llmagent.WithDescription("Assistant sur le parc de stations Vélib' de Paris"),
		llmagent.WithInstruction(tools.SystemInstruction),
		llmagent.WithTools(registered),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: stream}),

		// Plafond d'itérations d'outils. Sans lui, un modèle qui s'entête à
		// chercher une station inexistante peut boucler et brûler des jetons
		// jusqu'au plafond du fournisseur. Quatre suffisent largement : aucune
		// des questions de référence n'a besoin de plus de deux appels.
		llmagent.WithMaxToolIterations(4),
	)

	// ── La persistance des conversations ─────────────────────────────────────
	// Le framework fournit un service de sessions PostgreSQL, et son interface
	// expose exactement les quatre opérations demandées par la spécification : créer,
	// lister, rouvrir, supprimer. J'ai donc supprimé la couche de persistance
	// que j'avais commencé à écrire à la main : elle aurait dupliqué ce code
	// avec un comportement de reprise moins fidèle à ce qu'attend le Runner.
	sessions, err := sessionpg.NewService(
		sessionpg.WithPostgresClientDSN(cfg.PostgresDSN),
	)
	if err != nil {
		return nil, fmt.Errorf("service de sessions PostgreSQL : %w", err)
	}

	r := runner.NewRunner(AppName, ag,
		runner.WithSessionService(sessions),
	)

	log.Info("agent prêt",
		"modele", cfg.ModelName,
		"outils", len(registered),
		"streaming", stream)
	log.Info("outils enregistrés\n" + tools.Describe(registered))

	return &Service{Runner: r, Sessions: sessions}, nil
}

// Close libère ce que le service possède.
func (s *Service) Close() error {
	if s.Runner != nil {
		return s.Runner.Close()
	}
	return nil
}

// SessionKey construit une clé de session.
func SessionKey(userID, sessionID string) session.Key {
	return session.Key{AppName: AppName, UserID: userID, SessionID: sessionID}
}

// UserKey construit une clé utilisateur, pour lister les conversations.
func UserKey(userID string) session.UserKey {
	return session.UserKey{AppName: AppName, UserID: userID}
}

// Ping vérifie que PostgreSQL répond, pour la route de santé.
//
// On teste par un appel réel plutôt qu'en gardant un booléen mis à jour au
// démarrage : une base qui répondait il y a une heure ne prouve rien.
func (s *Service) Ping(ctx context.Context) error {
	_, err := s.Sessions.ListSessions(ctx, UserKey("healthcheck"))
	return err
}

// stripReasoningContent retire le champ reasoning_content des messages
// « assistant » avant l'envoi au fournisseur.
//
// Les modeles raisonneurs (gpt-oss, qwen3, o1…) renvoient leur reflexion dans
// ce champ. Le framework la stocke et la rejoue dans l'historique au tour
// suivant, ce que Groq refuse avec un 400 « property 'reasoning_content' is
// unsupported ». Le framework n'expose pas d'option pour ne pas l'envoyer :
// WithReasoningContentBackfill ne couvre que le cas ou le champ est vide.
//
// Le raisonnement ne sert qu'au modele pendant son propre tour, jamais aux
// suivants. Le retirer de l'historique ne change donc pas les reponses, et les
// fournisseurs qui l'acceptent se contentent de ne plus le voir.
//
// SetExtraFields remplace la table entiere, et le framework ne s'en sert que
// pour ce champ : la vider suffit.
func stripReasoningContent(_ context.Context, req *openaisdk.ChatCompletionNewParams) {
	if req == nil {
		return
	}
	for i := range req.Messages {
		if a := req.Messages[i].OfAssistant; a != nil {
			a.SetExtraFields(map[string]any{})
		}
	}
}
