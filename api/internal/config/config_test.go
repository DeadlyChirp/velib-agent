package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// La configuration part dans les journaux au démarrage. Si la rédaction laisse
// passer un mot de passe, il se retrouve dans tout ce qui collecte les logs, et
// il faut le considérer comme compromis. Ces tests valent le coup d'être lus
// avant d'ajouter un champ à Redacted.
func TestRedactedNeLaissePasserAucunSecret(t *testing.T) {
	c := Config{
		ModelAPIKey: "sk-proj-CeciEstUnSecretQuiNeDoitPasFuiter",
		PostgresDSN: "postgres://velib:MotDePasseSecret@db:5432/velib?sslmode=disable",
		ModelName:   "gpt-4o-mini",
	}

	rendu := fmt.Sprint(c.Redacted())
	for _, secret := range []string{
		"CeciEstUnSecretQuiNeDoitPasFuiter",
		"MotDePasseSecret",
	} {
		if strings.Contains(rendu, secret) {
			t.Errorf("secret %q présent dans la configuration journalisée :\n%s", secret, rendu)
		}
	}

	// La rédaction doit rester DIAGNOSTIQUABLE : masquer au point de ne plus
	// pouvoir vérifier qu'on a chargé la bonne clé rend le journal inutile.
	if !strings.Contains(rendu, "sk-p") {
		t.Error("la clé est masquée au point qu'on ne peut plus l'identifier")
	}
	if !strings.Contains(rendu, "velib") {
		t.Error("le DSN est masqué au point qu'on ne voit plus la base visée")
	}
}

func TestRedactDSN(t *testing.T) {
	cas := []struct {
		nom, dsn string
		interdit string
	}{
		{"DSN complet", "postgres://u:motdepasse@h:5432/d", "motdepasse"},
		{"mot de passe contenant un @", "postgres://u:p@ss@h:5432/d", "p@ss"},
		{"sans identifiants", "postgres://h:5432/d", ""},
		{"chaîne vide", "", ""},
		{"pas une URL", "nimportequoi", ""},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got := redactDSN(c.dsn)
			if c.interdit != "" && strings.Contains(got, c.interdit) {
				t.Errorf("redactDSN(%q) = %q laisse passer le mot de passe", c.dsn, got)
			}
			if got == "" {
				t.Errorf("redactDSN(%q) rend une chaîne vide : le journal perd l'information", c.dsn)
			}
		})
	}
}

// Une clé trop courte pour être vraie ne doit pas être affichée du tout : sur
// une clé de six caractères, montrer « quatre premiers + deux derniers » revient
// à tout montrer.
func TestRedactedCleTropCourte(t *testing.T) {
	rendu := fmt.Sprint(Config{ModelAPIKey: "sk-123"}.Redacted())
	if strings.Contains(rendu, "sk-123") {
		t.Errorf("une clé courte est affichée en entier : %s", rendu)
	}
}

// La subtilité qui a produit un bug réel : docker-compose définit TOUJOURS les
// variables, parfois à la chaîne vide. Si env() distinguait « absente » de
// « vide », tous les défauts sauteraient dès qu'on passe par compose.
func TestEnvTraiteLeVideCommeAbsent(t *testing.T) {
	t.Setenv("CONFIG_TEST_VIDE", "")
	if got := env("CONFIG_TEST_VIDE", "défaut"); got != "défaut" {
		t.Errorf("env sur une variable vide = %q, attendu le défaut", got)
	}
	t.Setenv("CONFIG_TEST_ESPACES", "   ")
	if got := env("CONFIG_TEST_ESPACES", "défaut"); got != "défaut" {
		t.Errorf("env sur des espaces = %q, attendu le défaut", got)
	}
	t.Setenv("CONFIG_TEST_VALEUR", "  réelle  ")
	if got := env("CONFIG_TEST_VALEUR", "défaut"); got != "réelle" {
		t.Errorf("env = %q, attendu la valeur détourée", got)
	}
}

// Le bug trouvé par les tests d'intégration : sans URL de base explicite, le
// client construit une URL relative et échoue sur « unsupported protocol
// scheme "" ». Ce test fige le défaut.
func TestURLDeBaseParDefautToujoursAbsolue(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("POSTGRES_DSN", "postgres://u:p@h:5432/d")
	t.Setenv("OPENAI_BASE_URL", "") // ce que fait docker-compose

	c, err := Load()
	if err != nil {
		t.Fatalf("Load : %v", err)
	}
	if !strings.HasPrefix(c.ModelBaseURL, "http") {
		t.Errorf("ModelBaseURL = %q : une URL sans schéma produit "+
			"« unsupported protocol scheme »", c.ModelBaseURL)
	}
}

func TestEnvDuration(t *testing.T) {
	cas := []struct {
		valeur  string
		attendu time.Duration
	}{
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"45", 45 * time.Second}, // forme sans unité, celle qu'on écrit spontanément
		{"n'importe quoi", 60 * time.Second},
		{"", 60 * time.Second},
	}
	for _, c := range cas {
		t.Run(c.valeur, func(t *testing.T) {
			t.Setenv("CONFIG_TEST_DUREE", c.valeur)
			if got := envDuration("CONFIG_TEST_DUREE", 60*time.Second); got != c.attendu {
				t.Errorf("envDuration(%q) = %v, attendu %v", c.valeur, got, c.attendu)
			}
		})
	}
}

// Une configuration invalide doit dire QUOI faire. « invalid configuration »
// tout court fait perdre dix minutes à qui démarre le projet.
func TestLoadDitCeQuiManque(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("POSTGRES_DSN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("aucune erreur alors que la clé et le DSN manquent")
	}
	msg := err.Error()
	for _, attendu := range []string{"OPENAI_API_KEY", "POSTGRES_DSN", ".env"} {
		if !strings.Contains(msg, attendu) {
			t.Errorf("le message ne mentionne pas %q :\n%s", attendu, msg)
		}
	}
}
