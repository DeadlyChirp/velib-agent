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

// ─────────────────────────────────────────────────────────────────────────────
// La distinction « absente » / « posée à vide »
// ─────────────────────────────────────────────────────────────────────────────

// env() traite le vide comme une absence, ce qui est le bon comportement
// partout SAUF quand le vide est un choix.
//
// MODEL_TEMPERATURE vaut 0.1 par défaut, mais les modèles raisonneurs d'OpenAI
// rejettent toute température : il faut pouvoir dire « n'envoie rien ». Avec
// env(), écrire MODEL_TEMPERATURE= aurait rendu « 0.1 » et l'échappatoire
// documentée n'aurait pas fonctionné — sans que rien n'échoue au démarrage.
func TestEnvPoseeDistingueAbsentDeVide(t *testing.T) {
	// Absente : on prend le défaut.
	if got := envPosee("CONFIG_TEST_JAMAIS_POSEE", "0.1"); got != "0.1" {
		t.Errorf("variable absente = %q, attendu le défaut", got)
	}

	// Posée à vide : c'est un CHOIX, on le respecte.
	t.Setenv("CONFIG_TEST_POSEE_VIDE", "")
	if got := envPosee("CONFIG_TEST_POSEE_VIDE", "0.1"); got != "" {
		t.Errorf("variable posée à vide = %q, attendu la chaîne vide — "+
			"c'est toute la raison d'être de cette fonction", got)
	}

	// Le contraste avec env(), qui rendrait le défaut sur le même cas.
	if got := env("CONFIG_TEST_POSEE_VIDE", "0.1"); got != "0.1" {
		t.Errorf("env sur une variable vide = %q, attendu le défaut : "+
			"si ce comportement change, envPosee n'a plus de raison d'être", got)
	}

	t.Setenv("CONFIG_TEST_POSEE_VALEUR", "  0.7  ")
	if got := envPosee("CONFIG_TEST_POSEE_VALEUR", "0.1"); got != "0.7" {
		t.Errorf("envPosee = %q, attendu la valeur détourée", got)
	}
}

// Une température illisible doit se voir AU DÉMARRAGE. Sans cette validation,
// le service part normalement et c'est le fournisseur qui rejette la première
// question, avec un message qui ne nomme pas la variable en cause.
func TestTemperatureIllisibleEstRefuseeAuDemarrage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("POSTGRES_DSN", "postgres://x:y@localhost:5432/z?sslmode=disable")
	t.Setenv("MODEL_TEMPERATURE", "tiède")

	_, err := Load()
	if err == nil {
		t.Fatal("une température non numérique a été acceptée")
	}
	if !strings.Contains(err.Error(), "MODEL_TEMPERATURE") {
		t.Errorf("le message ne nomme pas la variable fautive :\n%s", err)
	}

	// Et la vider doit rester valide : c'est l'échappatoire documentée.
	t.Setenv("MODEL_TEMPERATURE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("une température vide devrait être valide : %v", err)
	}
	if cfg.ModelTemperature != "" {
		t.Errorf("ModelTemperature = %q, attendu vide", cfg.ModelTemperature)
	}
	if r := cfg.Redacted()["temperature"]; r != "(non envoyée)" {
		t.Errorf("journal = %v, attendu « (non envoyée) »", r)
	}
}
