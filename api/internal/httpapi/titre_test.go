package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// deriveTitle découpe en RUNES et non en octets. Le piège est documenté dans le
// code, mais rien ne le protégeait : couper à l'octet 60 au milieu d'un « é »
// produit une séquence UTF-8 invalide, rendue « � » — et le titre est persisté,
// donc la corruption est définitive.
//
// Le cas est facile à réintroduire : « len(s) » est le réflexe naturel en Go, et
// il compte des octets. Une question en français dépasse les 60 caractères sans
// effort, et une station sur deux porte un accent.
func TestDeriveTitreResteDeLUTF8Valide(t *testing.T) {
	cas := []string{
		"Combien de vélos électriques à la station Opéra Garnier et aux alentours immédiats ?",
		"éééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé",
		"Quelles sont les stations où il n'y a plus aucun vélo disponible ce matin à Paris ?",
		"Station Père-Lachaise — combien de bornes libres pour un vélo électrique aujourd'hui ?",
		"日本語のテキストでも壊れないことを確認する非常に長い質問文をここに書いておきます",
		"Combien de 🚲 sont disponibles à la station Châtelet en ce moment précis dans Paris ?",
	}

	for _, q := range cas {
		t.Run(q[:min(24, len(q))], func(t *testing.T) {
			titre := deriveTitle(q)

			if !utf8.ValidString(titre) {
				t.Fatalf("titre en UTF-8 invalide : %q", titre)
			}
			if strings.ContainsRune(titre, '�') {
				t.Errorf("caractère de remplacement dans le titre : %q", titre)
			}
			if n := utf8.RuneCountInString(titre); n > 61 { // 60 + l'ellipse
				t.Errorf("titre de %d runes, plafond 60 (+ ellipse) : %q", n, titre)
			}
		})
	}
}

// Une question courte doit ressortir intacte, sans ellipse ajoutée par excès de
// zèle : un titre qui promet une suite qui n'existe pas est trompeur.
func TestDeriveTitreCourtIntact(t *testing.T) {
	cas := map[string]string{
		"Combien de stations ?":        "Combien de stations ?",
		"  espaces   en    trop   ":    "espaces en trop",
		"saut\nde\nligne":              "saut de ligne",
		"Vélos à Châtelet aujourd'hui": "Vélos à Châtelet aujourd'hui",
	}
	for entree, attendu := range cas {
		if got := deriveTitle(entree); got != attendu {
			t.Errorf("deriveTitle(%q)\n  = %q\n  veut %q", entree, got, attendu)
		}
	}
}

// La coupe cherche un espace pour ne pas trancher un mot en deux. Sur un texte
// sans aucun espace — une URL collée, par exemple — elle doit quand même couper
// plutôt que rendre le texte entier.
func TestDeriveTitreSansEspace(t *testing.T) {
	long := strings.Repeat("a", 200)
	titre := deriveTitle(long)

	if n := utf8.RuneCountInString(titre); n > 61 {
		t.Errorf("un texte sans espace n'est pas coupé : %d runes", n)
	}
	if !strings.HasSuffix(titre, "…") {
		t.Errorf("pas d'ellipse alors que le texte est tronqué : %q", titre)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
