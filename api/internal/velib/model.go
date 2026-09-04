// Package velib parle à la source de données publique Vélib' et en fait un modèle
// exploitable : téléchargement, jointure, cache, puis agrégations.
//
// La règle qui gouverne tout le paquet : le parc entier ne quitte jamais ce paquet.
// Les 748 Ko bruts sont réduits ici, en Go, et seuls de petits résultats bornés
// remontent vers les outils de l'agent — donc vers la fenêtre de contexte du modèle.
package velib

import (
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Les deux formes brutes renvoyées par l'API. Elles ne servent qu'au décodage :
// dès la jointure, on passe au modèle métier ci-dessous.
// ─────────────────────────────────────────────────────────────────────────────

type rawInformationFeed struct {
	LastUpdated int64 `json:"lastUpdatedOther"`
	TTL         int   `json:"ttl"`
	Data        struct {
		Stations []rawInformation `json:"stations"`
	} `json:"data"`
}

type rawInformation struct {
	StationID   int64   `json:"station_id"`
	StationCode string  `json:"stationCode"`
	Name        string  `json:"name"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Capacity    int     `json:"capacity"`
}

type rawStatusFeed struct {
	LastUpdated int64 `json:"lastUpdatedOther"`
	TTL         int   `json:"ttl"`
	Data        struct {
		Stations []rawStatus `json:"stations"`
	} `json:"data"`
}

type rawStatus struct {
	StationID    int64 `json:"station_id"`
	NumBikes     int   `json:"num_bikes_available"`
	NumDocks     int   `json:"num_docks_available"`
	IsInstalled  int   `json:"is_installed"`
	IsRenting    int   `json:"is_renting"`
	IsReturning  int   `json:"is_returning"`
	LastReported int64 `json:"last_reported"`

	// ⚠️ PIÈGE VÉRIFIÉ SUR LES DONNÉES RÉELLES (27/08/2026).
	// Ce champ n'est PAS un objet {"mechanical": 3, "ebike": 4} mais un TABLEAU
	// d'objets à une seule clé : [{"mechanical": 3}, {"ebike": 4}].
	// Un décodage en map[string]int échoue silencieusement en renvoyant zéro.
	// On décode donc en tranche de maps, et bikeTypes() fusionne par clé plutôt
	// que de se fier à l'ordre des éléments.
	NumBikesByType []map[string]int `json:"num_bikes_available_types"`
}

// bikeTypes aplatit le tableau biscornu en une map utilisable.
func (r rawStatus) bikeTypes() (mechanical, ebike int) {
	for _, entry := range r.NumBikesByType {
		mechanical += entry["mechanical"]
		ebike += entry["ebike"]
	}
	return mechanical, ebike
}

// ─────────────────────────────────────────────────────────────────────────────
// Le modèle métier : une station est la JOINTURE des deux flux.
// ─────────────────────────────────────────────────────────────────────────────

// Station est une station Vélib' complète, après jointure sur station_id.
//
// Le nom et la capacité viennent du référentiel, la disponibilité de l'état
// temps réel. Aucune question intéressante ne peut être répondue avec un seul
// des deux fichiers : « combien de vélos à Benjamin Godard » a besoin du nom
// (référentiel) ET du compte (état).
type Station struct {
	ID   int64   `json:"id"`
	Code string  `json:"code"`
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	// Capacity est la taille physique de la station.
	//
	// ⚠️ 4 stations du parc réel ont capacity = 0 (mesuré le 04/09). Aucun calcul
	// de ce paquet ne divise par ce champ, et les bornes libres sont LUES dans
	// num_docks_available plutôt que déduites par soustraction : 15 stations ont
	// bikes + docks > capacity, ce qui est physiquement impossible et rendrait
	// toute soustraction négative.
	Capacity int `json:"capacity"`

	BikesAvailable int `json:"bikes_available"`
	Mechanical     int `json:"mechanical"`
	Ebikes         int `json:"ebikes"`
	DocksAvailable int `json:"docks_available"`

	IsInstalled bool `json:"is_installed"`
	IsRenting   bool `json:"is_renting"`
	IsReturning bool `json:"is_returning"`

	// LastReported est l'instant où LA STATION a remonté son état, à ne pas
	// confondre avec la fraîcheur du fichier. Mesuré le 27/08 : âge médian de
	// 45 minutes, maximum de 5,5 ans (valeur sentinelle d'une station muette).
	LastReported time.Time `json:"last_reported"`

	// searchKey est le nom normalisé (minuscules, sans accents) utilisé par la
	// recherche. Calculé une fois à la jointure, pas à chaque requête.
	searchKey string
}

// OutOfService applique NOTRE définition de « hors service », que la spécification ne
// donnait pas.
//
// Trois drapeaux existent et donnent des résultats différents. Mesuré le 27/08
// sur les 1519 stations :
//
//	is_installed = 0 seul ......  1 station  (0,07 %)
//	is_renting   = 0 ..........  16 stations (1,05 %)
//	is_returning = 0 ..........  16 stations (1,05 %)
//
// Retenu : une station est hors service dès qu'elle ne rend plus le service
// attendu par un usager, c'est-à-dire si elle ne prête plus OU ne reprend plus
// de vélo, ou si elle est démontée. C'est la définition orientée usager, et
// elle est déclarée dans le README et dans la description de l'outil.
func (s Station) OutOfService() bool {
	return !s.IsInstalled || !s.IsRenting || !s.IsReturning
}

// IsEmpty : plus aucun vélo à prendre.
func (s Station) IsEmpty() bool { return s.BikesAvailable == 0 }

// IsFull : plus aucune borne libre pour rendre un vélo.
func (s Station) IsFull() bool { return s.DocksAvailable == 0 }

// AgeSeconds est l'ancienneté de la remontée de cette station.
func (s Station) AgeSeconds(now time.Time) int64 {
	if s.LastReported.IsZero() {
		return -1
	}
	return int64(now.Sub(s.LastReported).Seconds())
}

// ─────────────────────────────────────────────────────────────────────────────
// Le snapshot : l'état du parc à un instant donné.
// ─────────────────────────────────────────────────────────────────────────────

// Snapshot est le parc joint, figé à un instant. C'est l'unique entrée des
// agrégations, ce qui les rend testables sans réseau, sans base et sans LLM.
type Snapshot struct {
	Stations []Station `json:"stations"`

	// FetchedAt est l'instant du téléchargement, à distinguer de la fraîcheur
	// des stations elles-mêmes.
	FetchedAt time.Time `json:"fetched_at"`

	// Stale vaut true quand ce snapshot est servi depuis le cache alors que le
	// rafraîchissement a échoué. On préfère servir une donnée datée EN LE
	// DISANT plutôt que de renvoyer une erreur : une panne qui se tait coûte
	// plus cher qu'une panne qui crie.
	Stale bool `json:"stale"`

	// StaleReason porte la cause quand Stale est vrai, pour que l'agent puisse
	// l'expliquer à l'utilisateur au lieu d'inventer.
	StaleReason string `json:"stale_reason,omitempty"`
}

// AgeSeconds est l'ancienneté du snapshot lui-même.
func (s Snapshot) AgeSeconds(now time.Time) int64 {
	return int64(now.Sub(s.FetchedAt).Seconds())
}

// join fusionne les deux flux bruts en un snapshot.
//
// La jointure se fait sur station_id. Vérifié le 27/08 : les deux fichiers
// portent exactement les mêmes 1519 identifiants, en int, sans orphelin d'un
// côté ni de l'autre. On code quand même défensivement — une station présente
// dans le référentiel mais absente de l'état est conservée avec une
// disponibilité nulle et un LastReported vide, ce qui la marquera comme
// périmée plutôt que de la faire disparaître silencieusement du parc.
func join(info []rawInformation, status []rawStatus) []Station {
	byID := make(map[int64]rawStatus, len(status))
	for _, s := range status {
		byID[s.StationID] = s
	}

	out := make([]Station, 0, len(info))
	for _, i := range info {
		st := Station{
			ID:        i.StationID,
			Code:      i.StationCode,
			Name:      i.Name,
			Lat:       i.Lat,
			Lon:       i.Lon,
			Capacity:  i.Capacity,
			searchKey: normalize(i.Name),
		}
		if s, ok := byID[i.StationID]; ok {
			mech, ebike := s.bikeTypes()
			st.BikesAvailable = s.NumBikes
			st.Mechanical = mech
			st.Ebikes = ebike
			st.DocksAvailable = s.NumDocks
			st.IsInstalled = s.IsInstalled == 1
			st.IsRenting = s.IsRenting == 1
			st.IsReturning = s.IsReturning == 1
			if s.LastReported > 0 {
				st.LastReported = time.Unix(s.LastReported, 0)
			}
		}
		out = append(out, st)
	}
	return out
}
