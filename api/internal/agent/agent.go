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

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessionpg "trpc.group/trpc-go/trpc-agent-go/session/postgres"
	"trpc.group/trpc-go/trpc-agent-go/tool"

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
	log      *slog.Logger
}

// New construit le service complet.
func New(cfg config.Config, reg *tools.Registry, log *slog.Logger) (*Service, error) {
	// ── Le modèle ────────────────────────────────────────────────────────────
	// Compatible OpenAI, donc interchangeable par variable d'environnement :
	// OpenAI, Mistral, DeepSeek ou un Ollama local se branchent en changeant
	// MODEL_NAME et OPENAI_BASE_URL, sans recompiler.
	modelOpts := []openai.Option{openai.WithAPIKey(cfg.ModelAPIKey)}
	if cfg.ModelBaseURL != "" {
		modelOpts = append(modelOpts, openai.WithBaseURL(cfg.ModelBaseURL))
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

	return &Service{Runner: r, Sessions: sessions, log: log}, nil
}

// Close libère ce que le service possède.
func (s *Service) Close() error {
	if s.Runner != nil {
		return s.Runner.Close()
	}
	return nil
}

// ToolNames rend la liste des outils, pour la route de santé.
func ToolNames(ts []tool.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Declaration().Name)
	}
	return out
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
