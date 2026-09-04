package velib

import (
	"fmt"
	"time"
)

// Ce fichier contient TOUTES les agrégations du parc, et elles sont des
// fonctions pures : elles prennent un Snapshot et rendent une petite valeur.
//
// C'est la décision d'architecture centrale du projet. Elle a trois effets :
//
//  1. Le parc entier ne remonte jamais vers le modèle. 748 Ko et 1519 stations
//     entrent ici, quelques dizaines d'octets en sortent.
//  2. Ces fonctions se testent sans réseau, sans base et sans LLM. On peut
//     donc prouver que les cinq questions de référence ont la bonne réponse AVANT
//     de brancher quoi que ce soit d'agentique.
//  3. Un bug de calcul se débogue dans un test unitaire, pas en relisant des
//     traces de conversation.

// Freshness accompagne chaque réponse d'outil : sans elle, le modèle ne peut
// pas distinguer une donnée de trente secondes d'une donnée de trois heures, et
// il affirmera les deux avec la même assurance.
type Freshness struct {
	AgeSeconds  int64  `json:"age_seconds"`
	Stale       bool   `json:"stale"`
	StaleReason string `json:"stale_reason,omitempty"`
}

func freshnessOf(s Snapshot, now time.Time) Freshness {
	return Freshness{
		AgeSeconds:  s.AgeSeconds(now),
		Stale:       s.Stale,
		StaleReason: s.StaleReason,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Vue d'ensemble — répond aux questions 1 et 2 de référence
// ─────────────────────────────────────────────────────────────────────────────

// NetworkSummary est l'état agrégé du parc. Volontairement plat et court :
// c'est ce que le modèle reçoit à la place de 1519 stations.
type NetworkSummary struct {
	StationsTotal      int     `json:"stations_total"`
	StationsOutOfOrder int     `json:"stations_out_of_service"`
	OutOfServicePct    float64 `json:"out_of_service_pct"`
	StationsEmpty      int     `json:"stations_empty"`
	StationsFull       int     `json:"stations_full"`

	BikesTotal      int `json:"bikes_total"`
	BikesMechanical int `json:"bikes_mechanical"`
	BikesElectric   int `json:"bikes_electric"`
	DocksAvailable  int `json:"docks_available"`
	CapacityTotal   int `json:"capacity_total"`

	// OutOfServiceRule est renvoyée AU MODÈLE avec le chiffre. La spécification ne
	// définissait pas « hors service » et trois drapeaux donnent des réponses
	// différentes : le modèle doit pouvoir citer la règle appliquée plutôt que
	// d'énoncer un pourcentage sans contexte.
	OutOfServiceRule string `json:"out_of_service_rule"`

	Freshness Freshness `json:"freshness"`
}

// Summarize parcourt le parc une seule fois et produit tous les totaux.
func Summarize(s Snapshot, now time.Time) NetworkSummary {
	out := NetworkSummary{
		StationsTotal: len(s.Stations),
		OutOfServiceRule: "une station est comptée hors service si elle est démontée " +
			"(is_installed=0), ne prête plus (is_renting=0) ou ne reprend plus " +
			"de vélo (is_returning=0)",
		Freshness: freshnessOf(s, now),
	}

	for _, st := range s.Stations {
		if st.OutOfService() {
			out.StationsOutOfOrder++
		}
		if st.IsEmpty() {
			out.StationsEmpty++
		}
		if st.IsFull() {
			out.StationsFull++
		}
		out.BikesTotal += st.BikesAvailable
		out.BikesMechanical += st.Mechanical
		out.BikesElectric += st.Ebikes
		out.DocksAvailable += st.DocksAvailable
		out.CapacityTotal += st.Capacity
	}

	if out.StationsTotal > 0 {
		out.OutOfServicePct = round2(
			float64(out.StationsOutOfOrder) / float64(out.StationsTotal) * 100)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Classement — répond à la question 3
// ─────────────────────────────────────────────────────────────────────────────

// Metric est le critère de classement. Type fermé plutôt que chaîne libre :
// le modèle ne peut pas inventer un critère qui n'existe pas, et le schéma
// JSON généré pour l'outil énumère les valeurs valides.
type Metric string

const (
	MetricDocksAvailable Metric = "docks_available"
	MetricBikesAvailable Metric = "bikes_available"
	MetricEbikes         Metric = "ebikes"
	MetricCapacity       Metric = "capacity"
)

// ValidMetrics est la liste exposée dans la description de l'outil.
var ValidMetrics = []Metric{
	MetricDocksAvailable, MetricBikesAvailable, MetricEbikes, MetricCapacity,
}

func (m Metric) valid() bool {
	for _, v := range ValidMetrics {
		if v == m {
			return true
		}
	}
	return false
}

func (m Metric) value(s Station) int {
	switch m {
	case MetricDocksAvailable:
		return s.DocksAvailable
	case MetricBikesAvailable:
		return s.BikesAvailable
	case MetricEbikes:
		return s.Ebikes
	case MetricCapacity:
		return s.Capacity
	}
	return 0
}

// StationBrief est la forme réduite d'une station telle qu'elle remonte au
// modèle. On n'envoie ni latitude, ni longitude, ni drapeaux bruts : ce sont
// des octets de contexte qui ne servent à aucune des questions posées.
type StationBrief struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	BikesAvailable int    `json:"bikes_available"`
	Ebikes         int    `json:"ebikes"`
	DocksAvailable int    `json:"docks_available"`
	Capacity       int    `json:"capacity"`
	OutOfService   bool   `json:"out_of_service,omitempty"`

	// DataAgeSeconds n'est renseigné que quand la remontée de CETTE station est
	// anormalement vieille. Le silence est le cas nominal : ajouter un champ à
	// 1519 lignes pour dire « tout va bien » gaspille du contexte.
	DataAgeSeconds int64 `json:"data_age_seconds,omitempty"`
}

// staleStationThreshold : au-delà, on signale l'âge de la remontée au modèle.
//
// Mesuré le 27/08 sur le parc réel : âge médian 2746 s (~45 min), maximum
// 173 866 480 s (~5,5 ans, valeur sentinelle d'une station qui n'a jamais rien
// remonté), et 17 stations muettes depuis plus d'une heure. La documentation des API
// annonce un rafraîchissement « toutes les minutes » : c'est vrai du FICHIER,
// pas de chaque station. Le seuil est donc mis à une heure.
const staleStationThreshold = 3600

// MaxNameRunes borne la longueur d'un nom de station AU MOMENT OÙ IL ENTRE
// DANS LE CONTEXTE du modèle.
//
// Les noms viennent d'une source externe qu'on ne maîtrise pas. Mesuré : un
// seul nom de 100 000 caractères fait passer la sortie de count_stations de
// 1 590 à 105 652 octets, soit vingt-cinq fois le plafond que tout le reste du
// projet s'impose. Une faute de saisie chez l'opérateur suffit, sans même
// supposer de malveillance.
//
// 80 runes couvrent très largement le nom réel le plus long du parc parisien.
const MaxNameRunes = 80

// bornerNom tronque en RUNES et non en octets : couper au milieu d'un caractère
// accentué produirait une séquence UTF-8 invalide, servie telle quelle au
// modèle puis au navigateur.
func bornerNom(nom string) string {
	r := []rune(nom)
	if len(r) <= MaxNameRunes {
		return nom
	}
	return string(r[:MaxNameRunes]) + "…"
}

func brief(s Station, now time.Time) StationBrief {
	b := StationBrief{
		Code:           s.Code,
		Name:           bornerNom(s.Name),
		BikesAvailable: s.BikesAvailable,
		Ebikes:         s.Ebikes,
		DocksAvailable: s.DocksAvailable,
		Capacity:       s.Capacity,
		OutOfService:   s.OutOfService(),
	}
	if age := s.AgeSeconds(now); age < 0 || age > staleStationThreshold {
		b.DataAgeSeconds = age
	}
	return b
}

// RankResult est le retour d'un classement.
type RankResult struct {
	Metric    Metric         `json:"metric"`
	Order     string         `json:"order"`
	Stations  []StationBrief `json:"stations"`
	Freshness Freshness      `json:"freshness"`
	Note      string         `json:"note,omitempty"`
}

// MaxRankLimit borne la taille d'un classement CÔTÉ SERVEUR.
//
// Le modèle peut demander « les 500 premières » : c'est notre garde-fou qui
// refuse, pas sa bonne volonté. Un outil dont la taille de sortie dépend d'un
// paramètre choisi par le modèle est un outil qui finira par saturer le
// contexte le jour où la question sera formulée autrement.
const MaxRankLimit = 20

// Rank trie le parc selon une métrique et n'en renvoie que le sommet.
func Rank(s Snapshot, m Metric, limit int, ascending bool, now time.Time) RankResult {
	res := RankResult{Metric: m, Freshness: freshnessOf(s, now)}
	if ascending {
		res.Order = "ascending"
	} else {
		res.Order = "descending"
	}

	if !m.valid() {
		res.addNote("métrique inconnue, aucun classement produit")
		return res
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > MaxRankLimit {
		limit = MaxRankLimit
		res.addNote("limite ramenée à 20 par le serveur")
	}

	// ⚠️ CORRECTION D'UN DÉFAUT MESURÉ. Une station hors service annonce souvent
	// beaucoup de bornes libres, précisément parce qu'elle ne reprend plus de
	// vélo. Sans ce filtre, le top 5 par bornes libres remontait sur les données
	// réelles du 04/09 trois stations où l'on ne peut RIEN rendre : « Station
	// Tour de France » (200 bornes, donnée de 955 h), « Championnats d'Europe de
	// Natation » (200 bornes, 449 h) et « Hippodrome de Vincennes » (97 bornes,
	// 295 jours). La réponse à la question 3 de référence était donc fausse, et
	// fausse avec assurance.
	//
	// Le filtre ne s'applique PAS à capacity : cette métrique décrit la taille
	// physique de la station et non le service qu'elle rend. « Quelle est la plus
	// grande station » garde une réponse même si elle est fermée aujourd'hui.
	excluded := 0
	idx := make([]int, 0, len(s.Stations))
	for i, st := range s.Stations {
		if m != MetricCapacity && st.OutOfService() {
			excluded++
			continue
		}
		idx = append(idx, i)
	}
	// Selection bornee plutot qu'un tri complet.
	//
	// Mesure a 1 519 000 stations (mille fois le parc parisien) : le tri de
	// TOUTES les stations pour en rendre au plus vingt prenait 1096 ms. On ne
	// trie plus que les k retenus, et le cout redevient lineaire.
	//
	// C'est le seul endroit du projet ou la complexite depassait O(n), et ca ne
	// se voyait pas a 1 519 stations : 575 us, personne ne regarde.
	idx = topK(idx, limit, func(a, b int) bool {
		va, vb := m.value(s.Stations[a]), m.value(s.Stations[b])
		if va == vb {
			// Depart des ex aequo par le nom : sans cet ordre total, deux
			// executions sur la meme donnee peuvent rendre deux classements
			// differents, et un test qui en depend echoue un jour sans raison.
			return s.Stations[a].Name < s.Stations[b].Name
		}
		if ascending {
			return va < vb
		}
		return va > vb
	})

	if limit > len(idx) {
		limit = len(idx)
	}
	res.Stations = make([]StationBrief, 0, limit)
	for _, i := range idx[:limit] {
		res.Stations = append(res.Stations, brief(s.Stations[i], now))
	}

	if excluded > 0 {
		res.addNote(fmt.Sprintf("%d stations hors service exclues du classement "+
			"car elles ne rendent pas le service mesuré", excluded))
	}

	// Les ex aequo sont départagés par ordre alphabétique, ce qui est arbitraire.
	// Sur « les stations avec le moins de vélos », des dizaines sont à zéro : le
	// modèle doit savoir qu'il regarde un échantillon d'ex aequo et non un
	// palmarès, sinon il présente vingt noms comme LE classement.
	if len(res.Stations) > 0 && limit < len(idx) {
		last := m.value(s.Stations[idx[limit-1]])
		ties := 0
		for _, i := range idx[limit:] {
			if m.value(s.Stations[i]) == last {
				ties++
			}
		}
		if ties > 0 {
			res.addNote(fmt.Sprintf("%d autres stations ont la même valeur que la "+
				"dernière du classement, le départage est alphabétique donc "+
				"arbitraire", ties))
		}
	}
	return res
}

// addNote empile un message sans écraser le précédent.
//
// Note est un champ unique qui portait déjà « métrique inconnue » et « limite
// ramenée à 20 ». Une affectation directe aurait fait disparaître le signal de
// bridage au profit du dernier message écrit.
func (r *RankResult) addNote(msg string) {
	if r.Note == "" {
		r.Note = msg
		return
	}
	r.Note += ". " + msg
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Comptage avec échantillon — répond à la question 5
// ─────────────────────────────────────────────────────────────────────────────

// Filter est un critère de sélection. Fermé, comme Metric.
type Filter string

const (
	FilterEmpty        Filter = "empty"
	FilterFull         Filter = "full"
	FilterOutOfService Filter = "out_of_service"
	FilterHasEbikes    Filter = "has_ebikes"
)

var ValidFilters = []Filter{
	FilterEmpty, FilterFull, FilterOutOfService, FilterHasEbikes,
}

func (f Filter) matches(s Station) (bool, bool) {
	switch f {
	case FilterEmpty:
		return s.IsEmpty(), true
	case FilterFull:
		return s.IsFull(), true
	case FilterOutOfService:
		return s.OutOfService(), true
	case FilterHasEbikes:
		return s.Ebikes > 0, true
	}
	return false, false
}

// CountResult est LA réponse à « liste-moi toutes les stations vides ».
//
// C'est la décision de conception la plus importante du projet, et celle à
// défendre en soutenance. La question demande une liste non bornée. Mesuré le
// 27/08 : 49 stations vides, soit 3,2 % du parc. Aujourd'hui c'est tenable, un
// dimanche soir de pluie ce sera trois fois plus.
//
// Renvoyer la liste complète, c'est reproduire exactement le problème que la
// spécification demande de résoudre. Renvoyer un TOTAL plus un ÉCHANTILLON répond
// mieux à l'intention réelle de l'utilisateur — qui veut d'abord savoir
// combien — tout en gardant une taille de sortie constante quelle que soit la
// météo.
type CountResult struct {
	Filter    Filter         `json:"filter"`
	Total     int            `json:"total"`
	Pct       float64        `json:"pct_of_network"`
	Sample    []StationBrief `json:"sample"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note,omitempty"`
	Freshness Freshness      `json:"freshness"`
}

// MaxSampleSize borne l'échantillon, encore une fois côté serveur.
const MaxSampleSize = 10

// Count compte les stations correspondant au filtre et en renvoie un échantillon.
func Count(s Snapshot, f Filter, sampleSize int, now time.Time) CountResult {
	res := CountResult{Filter: f, Freshness: freshnessOf(s, now)}

	if _, ok := f.matches(Station{}); !ok {
		res.Note = "filtre inconnu, aucun comptage produit"
		return res
	}
	if sampleSize < 0 {
		sampleSize = 0
	}
	if sampleSize > MaxSampleSize {
		sampleSize = MaxSampleSize
	}

	res.Sample = make([]StationBrief, 0, sampleSize)
	for _, st := range s.Stations {
		ok, _ := f.matches(st)
		if !ok {
			continue
		}
		res.Total++
		if len(res.Sample) < sampleSize {
			res.Sample = append(res.Sample, brief(st, now))
		}
	}

	if len(s.Stations) > 0 {
		res.Pct = round2(float64(res.Total) / float64(len(s.Stations)) * 100)
	}
	res.Truncated = res.Total > len(res.Sample)
	if res.Truncated {
		res.Note = "échantillon partiel : le total est exact, la liste ne l'est pas"
	}
	return res
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Détail d'une station — répond à la question 4
// ─────────────────────────────────────────────────────────────────────────────

// StationDetail est le retour de la recherche par nom.
type StationDetail struct {
	Query string `json:"query"`

	// MatchCount est le nombre de stations RENVOYÉES, borné à MaxCandidates.
	MatchCount int `json:"match_count"`

	// TotalMatches est le nombre RÉEL de correspondances dans le parc.
	//
	// ⚠️ Les deux champs sont distincts à dessein. Une requête large comme
	// « place » ou « gare » correspond à des dizaines de stations : sans ce
	// compteur, le modèle croirait qu'il en existe trois et annoncerait un
	// résultat complet alors que la station visée peut être absente de la liste.
	TotalMatches int `json:"total_matches"`

	Stations  []StationBrief `json:"stations"`
	Ambiguous bool           `json:"ambiguous"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note,omitempty"`
	Freshness Freshness      `json:"freshness"`
}

// MaxCandidates borne le nombre de stations renvoyées par une recherche.
const MaxCandidates = 3

// FindStations cherche par nom et renvoie les candidats.
func FindStations(s Snapshot, query string, now time.Time) StationDetail {
	res := StationDetail{Query: query, Freshness: freshnessOf(s, now)}

	// On demande les MaxCandidates meilleurs ET le total : searchTop compte
	// tout mais ne materialise que ce qu'on montre. Construire la liste
	// complete pour n'en garder que trois coutait 61 Mo par recherche a
	// 1 519 000 stations.
	matches, total := searchTop(s.Stations, query, MaxCandidates)
	res.TotalMatches = total
	if total > len(matches) {
		res.Truncated = true
	}
	res.MatchCount = len(matches)
	for _, m := range matches {
		res.Stations = append(res.Stations, brief(m.Station, now))
	}

	switch {
	case len(matches) == 0:
		res.Note = "aucune station ne correspond, ne pas inventer de résultat " +
			"et proposer à l'utilisateur de reformuler"
	case len(matches) > 1:
		res.Ambiguous = true
		res.Note = "plusieurs stations correspondent, demander laquelle plutôt " +
			"que d'en choisir une"
	}

	if res.Truncated {
		res.Note = fmt.Sprintf("%d stations correspondent au total, seules les %d "+
			"premières sont montrées. Si la station cherchée n'y est pas, demander "+
			"un nom plus précis plutôt que de choisir dans cette liste",
			res.TotalMatches, res.MatchCount)
	}
	return res
}

// round2 arrondit à deux décimales. Un pourcentage à quinze décimales dans le
// contexte est du bruit que le modèle recopiera tel quel.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
