// Package config lit la configuration depuis l'environnement.
//
// Tout est lu au démarrage et validé UNE FOIS. Un service qui découvre à la
// première requête qu'il lui manque une clé a déjà échoué : il faut le savoir
// au lancement du conteneur, pas quand un utilisateur pose sa première question.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config est la configuration complète du service.
type Config struct {
	// HTTP
	Addr        string
	CORSOrigins []string

	// Modèle. La spécification impose que le modèle soit choisi par variable
	// d'environnement : n'importe quel fournisseur compatible OpenAI convient,
	// y compris Ollama en local, en changeant seulement BaseURL.
	ModelName    string
	ModelAPIKey  string
	ModelBaseURL string

	// ReasoningEffort pilote la longueur du raisonnement des modeles qui en
	// font (gpt-oss, o-series). Mesure sur gpt-oss-120b : « low » divise par
	// deux la latence ET les jetons de completion, sans perte visible ici —
	// ce sont les outils qui calculent, le modele ne fait que choisir l'outil
	// et rediger.
	//
	// Vide par defaut, donc RIEN n'est envoye : le champ est propre a certains
	// fournisseurs et un Mistral ou un Ollama rejetterait un parametre inconnu.
	ReasoningEffort string

	// PostgreSQL
	PostgresDSN string

	// Vélib'
	VelibInformationURL string
	VelibStatusURL      string
	VelibCacheTTL       time.Duration

	// Journalisation
	LogLevel  string
	LogFormat string
}

// Load lit l'environnement et valide.
func Load() (Config, error) {
	c := Config{
		Addr: env("API_ADDR", ":8080"),

		// Défaut restrictif plutôt que « * ». Par le chemin nominal — docker
		// compose puis localhost:3000 — le front appelle /api en relatif sur
		// nginx, même origine, et l'en-tête n'est jamais évalué. Un « * » par
		// défaut n'aurait donc rien débloqué pour l'usage prévu, tout en ouvrant
		// l'API à n'importe quelle page ouverte dans le navigateur.
		CORSOrigins: splitAndTrim(env("CORS_ORIGINS", "http://localhost:3000")),

		ModelName:    env("MODEL_NAME", "gpt-4o-mini"),
		ModelAPIKey:  env("OPENAI_API_KEY", ""),
		ModelBaseURL: env("OPENAI_BASE_URL", ""),

		ReasoningEffort: env("REASONING_EFFORT", ""),

		PostgresDSN: env("POSTGRES_DSN", ""),

		VelibInformationURL: env("VELIB_INFORMATION_URL",
			"https://velib-metropole-opendata.smovengo.cloud/opendata/Velib_Metropole/station_information.json"),
		VelibStatusURL: env("VELIB_STATUS_URL",
			"https://velib-metropole-opendata.smovengo.cloud/opendata/Velib_Metropole/station_status.json"),
		VelibCacheTTL: envDuration("VELIB_CACHE_TTL", 60*time.Second),

		LogLevel:  env("LOG_LEVEL", "info"),
		LogFormat: env("LOG_FORMAT", "text"),
	}

	// Validation stricte, avec des messages qui disent quoi faire. Un
	// « invalid configuration » sans détail fait perdre dix minutes à celui qui
	// démarre le projet pour la première fois.
	var problems []string
	if c.ModelAPIKey == "" {
		problems = append(problems,
			"OPENAI_API_KEY est vide : copier .env.example vers .env et y mettre une clé")
	}
	if c.PostgresDSN == "" {
		problems = append(problems,
			"POSTGRES_DSN est vide : le compose la fournit, en local utiliser "+
				"postgres://velib:velib@localhost:5432/velib?sslmode=disable")
	}
	if c.VelibCacheTTL < time.Second {
		problems = append(problems,
			"VELIB_CACHE_TTL sous la seconde : la source est un service public "+
				"gratuit, rester raisonnable sur la fréquence d'appel")
	}
	if len(problems) > 0 {
		return c, fmt.Errorf("configuration invalide :\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	return c, nil
}

// Redacted rend la configuration journalisable, sans la clé d'API.
//
// Journaliser une configuration complète au démarrage est utile ; y laisser un
// secret l'est beaucoup moins le jour où les journaux partent ailleurs.
func (c Config) Redacted() map[string]any {
	key := "(absente)"
	if len(c.ModelAPIKey) > 8 {
		key = c.ModelAPIKey[:4] + "…" + c.ModelAPIKey[len(c.ModelAPIKey)-2:]
	} else if c.ModelAPIKey != "" {
		key = "(trop courte pour être valide)"
	}
	return map[string]any{
		"addr":            c.Addr,
		"model":           c.ModelName,
		"model_base_url":  orDefault(c.ModelBaseURL, "(défaut du fournisseur)"),
		"reasoning_effort": orDefault(c.ReasoningEffort, "(non envoyé)"),
		"model_api_key":   key,
		"postgres":        redactDSN(c.PostgresDSN),
		"velib_cache_ttl": c.VelibCacheTTL.String(),
		"cors_origins":    c.CORSOrigins,
	}
}

// redactDSN masque le mot de passe d'une chaîne de connexion.
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return "(configurée)"
	}
	return dsn[:scheme+3] + "***" + dsn[at:]
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	// On accepte « 90s » comme « 90 » : la seconde forme est celle que les gens
	// écrivent spontanément dans un .env.
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
