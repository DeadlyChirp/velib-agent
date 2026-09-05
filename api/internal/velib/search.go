package velib

import (
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

// searchTop cherche des stations par nom, de façon tolérante.
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
//
// Elle rend les `limit` meilleures correspondances ET le nombre TOTAL de
// correspondances, sans jamais matérialiser ni trier les autres.
//
// C'est exactement ce dont FindStations a besoin : un total exact à annoncer, et
// trois candidats à montrer. L'implémentation précédente construisait la liste
// complète des correspondances puis la triait en entier pour en garder trois.
//
// Mesuré à 1 519 000 stations : 61 Mo alloués et 147 ms par recherche, dont la
// quasi-totalité pour des résultats immédiatement jetés.
//
// limit <= 0 signifie « aucune borne ». Ce sens venait d'une fonction exportée
// Search, supprimee depuis parce que seuls les bancs l'appelaient : la
// convention, elle, est conservee, et FindStations s'y appuie.
func searchTop(stations []Station, query string, limit int) ([]Match, int) {
	q := normalize(query)
	if q == "" {
		return nil, 0
	}

	// Découpé UNE fois, hors de la boucle : voir le commentaire de scoreName.
	mots := strings.Fields(q)

	// On ne retient QUE les correspondances : un tableau de scores dimensionné
	// sur tout le parc coûterait 12 Mo par recherche à 1 519 000 stations, pour
	// quelques milliers de correspondances réelles.
	type candidat struct {
		station int
		score   int
	}
	cands := make([]candidat, 0, 64)
	for i := range stations {
		if score := scoreName(stations[i].normalizedName(), q, mots); score > 0 {
			cands = append(cands, candidat{station: i, score: score})
		}
	}
	total := len(cands)

	// Ordre : score décroissant, puis nom croissant. Le second critère n'est pas
	// cosmétique — sans ordre total, deux exécutions sur la même donnée peuvent
	// rendre deux classements différents, et un test qui en dépend échoue un
	// jour sans raison apparente.
	meilleur := func(a, b int) bool {
		if cands[a].score != cands[b].score {
			return cands[a].score > cands[b].score
		}
		return stations[cands[a].station].Name < stations[cands[b].station].Name
	}

	// topK travaille sur les POSITIONS dans cands, pas sur les indices de
	// stations : c'est ce qui permet de ne rien allouer à la taille du parc.
	pos := make([]int, len(cands))
	for i := range pos {
		pos[i] = i
	}

	k := limit
	if k <= 0 || k > len(pos) {
		k = len(pos)
	}

	out := make([]Match, 0, k)
	for _, p := range topK(pos, k, meilleur) {
		out = append(out, Match{Station: stations[cands[p].station], Score: cands[p].score})
	}
	return out, total
}

// scoreName note la correspondance entre un nom normalisé et une requête
// normalisée. Zéro signifie « pas de correspondance ».
//
// L'échelle est volontairement grossière — trois paliers — parce qu'elle ne
// sert qu'à ordonner des candidats déjà filtrés. Une distance de Levenshtein
// serait plus fine et beaucoup plus difficile à défendre : elle ferait
// remonter des noms qui ne partagent aucun mot avec la requête.
// ⚠️ words est passe en parametre et NON recalcule ici.
//
// strings.Fields(query) etait appele a l'interieur de cette fonction, donc une
// fois PAR STATION, pour une requete identique a chaque appel. Mesure a
// 1 519 000 stations : 1,5 million d'allocations jetees aussitot, et 242 ms par
// recherche. La requete ne change pas pendant le parcours : on la decoupe une
// fois, chez l'appelant.
//
// Le genre de detail invisible a 1 519 stations — 175 us, personne ne regarde —
// et qui domine tout le reste des qu'on change d'ordre de grandeur.
func scoreName(name, query string, words []string) int {
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
