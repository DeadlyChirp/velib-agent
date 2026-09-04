package httpapi

import (
	"strings"
	"testing"
)

// Une limite de debit est passagere, une cle refusee ne l'est pas. Les
// confondre derriere un message unique fait perdre du temps a l'utilisateur :
// l'un doit attendre, l'autre doit corriger sa configuration.
func TestMessageUtilisateur(t *testing.T) {
	cas := []struct {
		nom, brut, attendu string
	}{
		{"limite Groq",
			`POST "https://api.groq.com/...": 429 Too Many Requests {"message":"Rate limit reached"}`,
			"Le quota du fournisseur de modèle est atteint. Réessayez dans une minute."},
		{"cle refusee",
			`401 Unauthorized {"error":{"code":"invalid_api_key"}}`,
			"La clé d'API du modèle est refusée. Vérifiez OPENAI_API_KEY."},
		{"delai depasse",
			"context deadline exceeded",
			"Le modèle n'a pas répondu à temps. Réessayez."},
		{"inconnu",
			"boom",
			"Une erreur est survenue pendant la génération de la réponse."},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if got := messageUtilisateur(c.brut); got != c.attendu {
				t.Errorf("messageUtilisateur(%q)\n  = %q\n  veut %q", c.brut, got, c.attendu)
			}
		})
	}
}

// Le message brut du fournisseur contient l'URL et le nom de l'organisation.
// Rien de tout cela ne doit atteindre le navigateur.
func TestMessageUtilisateurNeFuitPas(t *testing.T) {
	brut := `POST "https://api.groq.com/openai/v1/chat/completions": 429 ` +
		`{"message":"Rate limit reached for model X in organization org_secret_123"}`
	got := messageUtilisateur(brut)
	for _, interdit := range []string{"api.groq.com", "org_secret_123", "chat/completions"} {
		if strings.Contains(got, interdit) {
			t.Errorf("le message utilisateur laisse fuiter %q : %s", interdit, got)
		}
	}
}
