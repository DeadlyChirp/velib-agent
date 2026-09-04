package velib

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// normalize met un nom de station sous une forme comparable : minuscules, sans
// accents, espaces réduits.
//
// ⚠️ PIÈGE VÉRIFIÉ : 532 des 1519 stations portent au moins un accent. Sans
// normalisation, un utilisateur qui tape « telegraphe » ne trouve jamais
// « Télégraphe », et la recherche paraît cassée alors que la donnée est là.
func normalize(s string) string {
	t := transform.Chain(
		norm.NFD,
		runes.Remove(runes.In(unicode.Mn)), // retire les diacritiques
		norm.NFC,
	)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s // en cas d'échec, on dégrade vers la casse seule plutôt que d'échouer
	}
	return strings.Join(strings.Fields(strings.ToLower(out)), " ")
}

// normalizedName rend le nom comparable, en préférant la valeur pré-calculée.
//
// ⚠️ CORRECTION issue d'un test. La version précédente lisait directement le
// champ privé searchKey, rempli uniquement par join(). Une Station construite
// autrement — dans un test, ou demain par un second chemin de chargement —
// avait donc une clé vide, et la recherche renvoyait « aucun résultat » sans
// la moindre erreur. Une structure à moitié initialisée dont la panne est
// silencieuse est précisément ce qu'il ne faut pas laisser dans un code.
//
// Le repli normalise à la volée : le chemin de production reste rapide, et
// aucun autre chemin ne peut plus échouer en silence.
func (s Station) normalizedName() string {
	if s.searchKey != "" {
		return s.searchKey
	}
	return normalize(s.Name)
}

// Match est une station trouvée par la recherche, avec son score.
type Match struct {
	Station Station
	// Score sert uniquement à ordonner les candidats, il n'est pas exposé au
	// modèle : un score numérique sans échelle l'inciterait à sur-interpréter.
	Score int
}

// Search cherche des stations par nom, de façon tolérante.
//
// Conception délibérée : cette fonction renvoie une LISTE de candidats, jamais
// « la » station. Trois raisons mesurées sur les données réelles :
//
//   - « Benjamin Godard » ne correspond à aucun nom exact ; le nom réel est
//     « Benjamin Godard - Victor Hugo ». Une égalité stricte échoue.
//   - « gare de lyon » correspond à TROIS stations distinctes (Place Louis
//     Armand, Chalon, Roland Barthes).
//   - Trois noms sont carrément en double dans le référentiel : « Place Nelson
//     Mandela », « Place de la Gare », « Verdun - Carnot ».
//
// Choisir arbitrairement parmi des candidats, c'est répondre faux avec
// assurance. L'agent doit pouvoir demander lequel.
func Search(stations []Station, query string, limit int) []Match {
	q := normalize(query)
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 3
	}

	var matches []Match
	for _, s := range stations {
		score := scoreName(s.normalizedName(), q)
		if score > 0 {
			matches = append(matches, Match{Station: s, Score: score})
		}
	}

	// Tri par score décroissant, puis par nom pour un ordre stable et
	// reproductible — un test qui dépend de l'ordre de parcours d'une map est
	// un test qui échouera un jour sans raison apparente.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Station.Name < matches[j].Station.Name
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// scoreName note la correspondance entre un nom normalisé et une requête
// normalisée. Zéro signifie « pas de correspondance ».
//
// L'échelle est volontairement grossière — trois paliers — parce qu'elle ne
// sert qu'à ordonner des candidats déjà filtrés. Une distance de Levenshtein
// serait plus fine et beaucoup plus difficile à défendre : elle ferait
// remonter des noms qui ne partagent aucun mot avec la requête.
func scoreName(name, query string) int {
	switch {
	case name == query:
		return 100 // exact
	case strings.HasPrefix(name, query):
		return 80 // « benjamin godard » -> « benjamin godard - victor hugo »
	case strings.Contains(name, query):
		return 60 // la requête est un fragment du nom
	}

	// Dernier filet : tous les mots de la requête sont présents, dans le
	// désordre. Couvre « godard victor » ou « hugo benjamin ».
	words := strings.Fields(query)
	if len(words) == 0 {
		return 0
	}
	for _, w := range words {
		if !strings.Contains(name, w) {
			return 0
		}
	}
	return 40
}
