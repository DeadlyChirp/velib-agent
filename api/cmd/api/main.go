// Commande api : le service complet.
//
// Ordre de démarrage voulu :
//  1. configuration lue et validée — on échoue ici, pas à la première requête ;
//  2. cache Vélib' préchauffé — le premier utilisateur ne paie pas deux
//     téléchargements pendant que le modèle attend ;
//  3. agent et sessions PostgreSQL ;
//  4. serveur HTTP, avec arrêt propre.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"velib-agent/internal/agent"
	"velib-agent/internal/config"
	"velib-agent/internal/httpapi"
	"velib-agent/internal/tools"
	"velib-agent/internal/velib"
)

func main() {
	// Mode sonde : le conteneur s'interroge lui-même.
	//
	// Le binaire sert de client de healthcheck, ce qui évite d'installer curl
	// ou wget dans l'image finale. Moins d'outils dans le conteneur, c'est
	// moins de surface d'attaque et une image plus légère.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}

	if err := run(); err != nil {
		slog.Error("démarrage impossible", "erreur", err)
		os.Exit(1)
	}
}

func run() error {
	// ── 1. Configuration ─────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		// Journal minimal : le logger définitif dépend de la configuration
		// qu'on vient justement de ne pas pouvoir lire.
		slog.Error("configuration", "erreur", err)
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("configuration chargée", "config", cfg.Redacted())

	// ── 2. Source Vélib' ─────────────────────────────────────────────────────
	client := velib.NewClient(
		velib.WithInformationURL(cfg.VelibInformationURL),
		velib.WithStatusURL(cfg.VelibStatusURL),
	)
	cache := velib.NewCache(client,
		velib.WithTTL(cfg.VelibCacheTTL),
		velib.WithLogger(log),
	)

	// Le préchauffage n'est PAS bloquant en cas d'échec : refuser de démarrer
	// parce qu'un service tiers est momentanément indisponible transformerait
	// une panne partielle en panne totale. Le cache réessaiera au premier appel.
	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	cache.Warm(warmCtx)
	cancelWarm()

	// ── 3. Agent ─────────────────────────────────────────────────────────────
	registry := tools.NewRegistry(cache, log)
	agentSvc, err := agent.New(cfg, registry, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := agentSvc.Close(); err != nil {
			log.Warn("fermeture de l'agent", "erreur", err)
		}
	}()

	// ── 4. Serveur HTTP ──────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: httpapi.New(agentSvc, cfg, log).Routes(),

		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,

		// ⚠️ PAS de WriteTimeout. Une réponse en streaming dure aussi longtemps
		// que le modèle écrit, et un WriteTimeout couperait le flux au milieu
		// d'une phrase — panne d'autant plus déroutante qu'elle ne se produit
		// que sur les réponses longues, donc jamais pendant les tests rapides.
		// La protection contre les clients qui traînent est assurée par
		// IdleTimeout et par l'annulation de contexte quand le client part.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("serveur démarré", "adresse", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// ── 5. Arrêt propre ──────────────────────────────────────────────────────
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("arrêt demandé", "signal", sig.String())
	}

	// Les conversations en cours ont le temps de se terminer et d'être
	// persistées. Sans ça, un simple redéploiement perdrait la réponse que
	// l'utilisateur était en train de lire.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("arrêt forcé après délai", "erreur", err)
		return srv.Close()
	}
	log.Info("arrêt propre")
	return nil
}

// healthcheck interroge la route de santé du service local.
//
// Renvoie 0 si le service est sain, 1 sinon — la convention attendue par la
// directive HEALTHCHECK de Docker.
func healthcheck() int {
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()

	// La route renvoie 503 quand PostgreSQL ne répond plus : le conteneur est
	// alors déclaré non sain, ce qui est le comportement voulu.
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// newLogger construit le journal structuré.
//
// Texte par défaut, lisible dans un terminal pendant le développement ; JSON
// sur demande, pour être ingérable par un agrégateur en production.
func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
