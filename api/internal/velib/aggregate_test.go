package velib

import (
	"encoding/json"
	"testing"
	"time"
)

// Ce fichier prouve que les cinq questions de référence ont la bonne réponse SANS
// modèle, SANS réseau et SANS base. C'est le jalon qui rend la suite mécanique :
// si un chiffre est faux ici, aucun prompt ne le rattrapera, et on le débogue
// en une seconde au lieu de relire des traces de conversation.
//
// Le parc de test est fabriqué à la main pour encoder TOUS les pièges mesurés
// sur les données réelles du 27/08/2026, plutôt que d'embarquer 748 Ko de
// fixture qu'on ne peut pas lire en revue.

var refTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// fixture construit un parc de 8 stations couvrant chaque piège.
func fixture() Snapshot {
	fresh := refTime.Add(-30 * time.Second)
	old := refTime.Add(-3 * time.Hour) // au-delà du seuil de signalement

	return Snapshot{
		FetchedAt: refTime.Add(-10 * time.Second),
		Stations: []Station{
			// 1. Station nominale, avec vélos des deux types.
			{ID: 1, Code: "16107", Name: "Benjamin Godard - Victor Hugo",
				searchKey: normalize("Benjamin Godard - Victor Hugo"),
				Capacity:  35, BikesAvailable: 7, Mechanical: 3, Ebikes: 4,
				DocksAvailable: 27,
				IsInstalled:    true, IsRenting: true, IsReturning: true,
				LastReported: fresh},

			// 2. PIÈGE capacity = 0 : toute division doit être protégée.
			{ID: 2, Code: "00001", Name: "Coysevox - Lamarck",
				searchKey: normalize("Coysevox - Lamarck"),
				Capacity:  0, BikesAvailable: 0, DocksAvailable: 0,
				IsInstalled: true, IsRenting: true, IsReturning: true,
				LastReported: fresh},

			// 3. PIÈGE homonymie : trois stains « Gare de Lyon » dans le parc réel.
			{ID: 3, Code: "12001", Name: "Gare de Lyon - Chalon",
				searchKey: normalize("Gare de Lyon - Chalon"),
				Capacity:  40, BikesAvailable: 12, Mechanical: 8, Ebikes: 4,
				DocksAvailable: 28,
				IsInstalled:    true, IsRenting: true, IsReturning: true,
				LastReported: fresh},
			{ID: 4, Code: "12002", Name: "Gare de Lyon - Roland Barthes",
				searchKey: normalize("Gare de Lyon - Roland Barthes"),
				Capacity:  30, BikesAvailable: 0, DocksAvailable: 30,
				IsInstalled: true, IsRenting: true, IsReturning: true,
				LastReported: fresh},

			// 4. PIÈGE accents : « Télégraphe » doit se trouver via « telegraphe ».
			{ID: 5, Code: "20001", Name: "Télégraphe - Belleville",
				searchKey: normalize("Télégraphe - Belleville"),
				Capacity:  25, BikesAvailable: 5, Mechanical: 2, Ebikes: 3,
				DocksAvailable: 20,
				IsInstalled:    true, IsRenting: true, IsReturning: true,
				LastReported: fresh},

			// 5. Hors service par is_renting, la combinaison (1,0,0) réelle.
			{ID: 6, Code: "75001", Name: "Station En Panne",
				searchKey: normalize("Station En Panne"),
				Capacity:  20, BikesAvailable: 3, Mechanical: 3,
				DocksAvailable: 17,
				IsInstalled:    true, IsRenting: false, IsReturning: false,
				LastReported: fresh},

			// 6. Hors service total, la combinaison (0,0,0) réelle.
			{ID: 7, Code: "75002", Name: "Station Demontee",
				searchKey: normalize("Station Demontee"),
				Capacity:  15, BikesAvailable: 0, DocksAvailable: 0,
				IsInstalled: false, IsRenting: false, IsReturning: false,
				LastReported: fresh},

			// 7. PIÈGE fraîcheur : remontée vieille de 3 h, doit être signalée.
			{ID: 8, Code: "93001", Name: "Station Muette",
				searchKey: normalize("Station Muette"),
				Capacity:  10, BikesAvailable: 2, Mechanical: 2,
				DocksAvailable: 8,
				IsInstalled:    true, IsRenting: true, IsReturning: true,
				LastReported: old},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Question 1 : combien de vélos électriques sur l'ensemble du parc
// Question 2 : quel pourcentage des stations est hors service
// ─────────────────────────────────────────────────────────────────────────────

func TestSummarize(t *testing.T) {
	got := Summarize(fixture(), refTime)

	// Q1 : 4 + 4 + 3 = 11 électriques.
	if got.BikesElectric != 11 {
		t.Errorf("vélos électriques = %d, attendu 11", got.BikesElectric)
	}
	// Cohérence : mécaniques + électriques = total. Vérifié vrai sur le parc
	// réel (11978 + 8500 = 20478), donc l'invariant doit tenir ici aussi.
	if got.BikesMechanical+got.BikesElectric != got.BikesTotal {
		t.Errorf("incohérence : %d mécaniques + %d électriques != %d au total",
			got.BikesMechanical, got.BikesElectric, got.BikesTotal)
	}

	// Q2 : 2 stations hors service sur 8 = 25 %.
	if got.StationsOutOfOrder != 2 {
		t.Errorf("hors service = %d, attendu 2", got.StationsOutOfOrder)
	}
	if got.OutOfServicePct != 25 {
		t.Errorf("pourcentage hors service = %v, attendu 25", got.OutOfServicePct)
	}

	// La règle appliquée doit remonter au modèle : un pourcentage sans sa
	// définition est un chiffre indéfendable.
	if got.OutOfServiceRule == "" {
		t.Error("la règle « hors service » doit accompagner le chiffre")
	}

	// Stations vides : ID 2, 4 et 7.
	if got.StationsEmpty != 3 {
		t.Errorf("stations vides = %d, attendu 3", got.StationsEmpty)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Question 3 : les cinq stations qui ont le plus de bornes libres
// ─────────────────────────────────────────────────────────────────────────────

func TestRank(t *testing.T) {
	got := Rank(fixture(), MetricDocksAvailable, 3, false, refTime)

	if len(got.Stations) != 3 {
		t.Fatalf("%d stations renvoyées, attendu 3", len(got.Stations))
	}
	// Ordre attendu : 30 (Roland Barthes), 28 (Chalon), 27 (Benjamin Godard).
	if got.Stations[0].DocksAvailable != 30 || got.Stations[1].DocksAvailable != 28 {
		t.Errorf("mauvais ordre : %d puis %d",
			got.Stations[0].DocksAvailable, got.Stations[1].DocksAvailable)
	}
	// Le tri doit être décroissant strictement.
	for i := 1; i < len(got.Stations); i++ {
		if got.Stations[i-1].DocksAvailable < got.Stations[i].DocksAvailable {
			t.Errorf("tri cassé en position %d", i)
		}
	}
}

// La borne serveur est un garde-fou, pas une suggestion : si le modèle demande
// 500 stations, c'est notre code qui refuse.
func TestRankLimitIsClampedServerSide(t *testing.T) {
	got := Rank(fixture(), MetricDocksAvailable, 500, false, refTime)

	if len(got.Stations) > MaxRankLimit {
		t.Errorf("%d stations renvoyées, la borne de %d n'a pas été appliquée",
			len(got.Stations), MaxRankLimit)
	}
	if got.Note == "" {
		t.Error("le bridage doit être annoncé au modèle, pas silencieux")
	}
}

func TestRankRejectsUnknownMetric(t *testing.T) {
	got := Rank(fixture(), Metric("nombre_de_licornes"), 5, false, refTime)

	if len(got.Stations) != 0 {
		t.Error("une métrique inconnue ne doit produire aucun classement")
	}
	if got.Note == "" {
		t.Error("le refus doit être expliqué au modèle")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Question 4 : combien de vélos à la station Benjamin Godard
// ─────────────────────────────────────────────────────────────────────────────

func TestFindStationPartialName(t *testing.T) {
	// L'utilisateur tape « Benjamin Godard », le nom réel porte un suffixe.
	got := FindStations(fixture(), "Benjamin Godard", refTime)

	if got.MatchCount != 1 {
		t.Fatalf("%d correspondances, attendu 1", got.MatchCount)
	}
	if got.Stations[0].BikesAvailable != 7 {
		t.Errorf("vélos = %d, attendu 7", got.Stations[0].BikesAvailable)
	}
	if got.Ambiguous {
		t.Error("une seule correspondance ne doit pas être marquée ambiguë")
	}
}

func TestFindStationIgnoresAccents(t *testing.T) {
	got := FindStations(fixture(), "telegraphe", refTime)

	if got.MatchCount != 1 {
		t.Fatalf("« telegraphe » ne trouve pas « Télégraphe » : %d correspondances",
			got.MatchCount)
	}
}

// Le cas qui distingue une réponse honnête d'une réponse assurée et fausse.
func TestFindStationAmbiguous(t *testing.T) {
	got := FindStations(fixture(), "gare de lyon", refTime)

	if got.MatchCount != 2 {
		t.Fatalf("%d correspondances, attendu 2", got.MatchCount)
	}
	if !got.Ambiguous {
		t.Error("plusieurs correspondances doivent être signalées comme ambiguës")
	}
	if got.Note == "" {
		t.Error("le modèle doit recevoir la consigne de demander laquelle")
	}
}

func TestFindStationNotFound(t *testing.T) {
	got := FindStations(fixture(), "Station Qui N Existe Pas", refTime)

	if got.MatchCount != 0 {
		t.Errorf("%d correspondances, attendu 0", got.MatchCount)
	}
	if got.Note == "" {
		t.Error("l'absence de résultat doit être explicite, sinon le modèle invente")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Question 5 : liste-moi toutes les stations vides
// ─────────────────────────────────────────────────────────────────────────────

func TestCountReturnsTotalNotList(t *testing.T) {
	got := Count(fixture(), FilterEmpty, 2, refTime)

	// Le TOTAL doit être exact même quand l'échantillon est tronqué : c'est
	// tout l'intérêt de la forme « compte + échantillon ».
	if got.Total != 3 {
		t.Errorf("total = %d, attendu 3", got.Total)
	}
	if len(got.Sample) != 2 {
		t.Errorf("échantillon = %d, attendu 2", len(got.Sample))
	}
	if !got.Truncated {
		t.Error("la troncature doit être signalée")
	}
	if got.Note == "" {
		t.Error("le modèle doit savoir que la liste est partielle mais le total exact")
	}
}

func TestCountSampleIsClampedServerSide(t *testing.T) {
	got := Count(fixture(), FilterEmpty, 9999, refTime)

	if len(got.Sample) > MaxSampleSize {
		t.Errorf("échantillon de %d, la borne de %d n'a pas été appliquée",
			len(got.Sample), MaxSampleSize)
	}
}

func TestCountOutOfService(t *testing.T) {
	got := Count(fixture(), FilterOutOfService, 5, refTime)

	if got.Total != 2 {
		t.Errorf("total hors service = %d, attendu 2", got.Total)
	}
	if got.Pct != 25 {
		t.Errorf("pourcentage = %v, attendu 25", got.Pct)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Les pièges des données réelles
// ─────────────────────────────────────────────────────────────────────────────

// 4 stations du parc réel ont capacity = 0 (mesuré le 04/09).
//
// La première version portait une méthode OccupancyRate() protégée contre la
// division par zéro. L'audit a montré qu'elle n'était appelée NULLE PART : du
// code mort, avec des tests et une place d'honneur dans le README. Elle a été
// retirée, et la vraie protection est vérifiée ici — aucune agrégation ne
// divise par Capacity, ni ne déduit les bornes libres par soustraction.
func TestNoAggregationDividesByCapacity(t *testing.T) {
	snap := fixture()

	// La fixture contient « Coysevox - Lamarck » avec capacity = 0, comme le
	// parc réel. Toutes les agrégations doivent produire des nombres finis.
	sum := Summarize(snap, refTime)
	if sum.StationsTotal == 0 {
		t.Fatal("fixture vide")
	}
	if sum.OutOfServicePct < 0 || sum.OutOfServicePct > 100 {
		t.Errorf("pourcentage aberrant : %v", sum.OutOfServicePct)
	}

	for _, m := range ValidMetrics {
		r := Rank(snap, m, 10, false, refTime)
		for _, st := range r.Stations {
			// Les bornes libres sont LUES, jamais calculées : 15 stations réelles
			// ont bikes + docks > capacity, ce qui rendrait toute soustraction
			// négative.
			if st.DocksAvailable < 0 {
				t.Errorf("%s : %d bornes libres, valeur négative issue d'un calcul",
					st.Name, st.DocksAvailable)
			}
		}
	}

	for _, f := range ValidFilters {
		c := Count(snap, f, 5, refTime)
		if c.Pct < 0 || c.Pct > 100 {
			t.Errorf("filtre %s : pourcentage aberrant %v", f, c.Pct)
		}
	}
}

// Une station à capacité nulle ne doit pas disparaître du parc pour autant :
// elle compte dans les totaux et peut être trouvée par son nom.
func TestZeroCapacityStationRemainsVisible(t *testing.T) {
	got := FindStations(fixture(), "Coysevox", refTime)

	if got.TotalMatches == 0 {
		t.Error("la station à capacité nulle a disparu de la recherche")
	}
}

// Le tableau [{"mechanical":3},{"ebike":4}] doit être aplati correctement.
func TestBikeTypesFlattensWeirdArray(t *testing.T) {
	var r rawStatus
	raw := `{"num_bikes_available_types":[{"mechanical":3},{"ebike":4}]}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("décodage : %v", err)
	}

	mech, ebike := r.bikeTypes()
	if mech != 3 || ebike != 4 {
		t.Errorf("mécaniques=%d électriques=%d, attendu 3 et 4", mech, ebike)
	}
}

// L'ordre des éléments ne doit pas compter : on fusionne par clé.
func TestBikeTypesIgnoresOrder(t *testing.T) {
	var r rawStatus
	raw := `{"num_bikes_available_types":[{"ebike":9},{"mechanical":1}]}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("décodage : %v", err)
	}

	mech, ebike := r.bikeTypes()
	if mech != 1 || ebike != 9 {
		t.Errorf("mécaniques=%d électriques=%d, attendu 1 et 9", mech, ebike)
	}
}

// Une remontée vieille de 3 h doit être signalée au modèle ; une remontée
// fraîche ne doit pas polluer le contexte avec un champ inutile.
func TestBriefFlagsStaleStationOnly(t *testing.T) {
	snap := fixture()

	var muette, godard StationBrief
	for _, s := range snap.Stations {
		switch s.Name {
		case "Station Muette":
			muette = brief(s, refTime)
		case "Benjamin Godard - Victor Hugo":
			godard = brief(s, refTime)
		}
	}

	if muette.DataAgeSeconds == 0 {
		t.Error("une remontée de 3 h doit porter son âge")
	}
	if godard.DataAgeSeconds != 0 {
		t.Errorf("une remontée fraîche ne doit pas porter d'âge, obtenu %d",
			godard.DataAgeSeconds)
	}
}

// La jointure doit survivre à une station présente au référentiel mais absente
// de l'état : on la garde à zéro plutôt que de la faire disparaître du parc.
func TestJoinKeepsStationMissingFromStatus(t *testing.T) {
	info := []rawInformation{
		{StationID: 1, Name: "Présente", Capacity: 10},
		{StationID: 2, Name: "Orpheline", Capacity: 20},
	}
	status := []rawStatus{
		{StationID: 1, NumBikes: 5, NumDocks: 5, IsInstalled: 1, IsRenting: 1, IsReturning: 1},
	}

	got := join(info, status)
	if len(got) != 2 {
		t.Fatalf("%d stations après jointure, attendu 2", len(got))
	}
	if got[1].Name != "Orpheline" || got[1].BikesAvailable != 0 {
		t.Error("la station sans état doit être conservée à zéro")
	}
	// Sans état, elle est hors service : les trois drapeaux sont faux.
	if !got[1].OutOfService() {
		t.Error("une station sans état remonté doit compter comme hors service")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// La taille de sortie : l'invariant qui protège la fenêtre de contexte
// ─────────────────────────────────────────────────────────────────────────────

// Ce test est le garde-fou du projet entier. Il échouera le jour où quelqu'un
// ajoutera un champ verbeux dans un retour d'outil, et rappellera pourquoi la
// limite existe.
func TestToolOutputsStaySmall(t *testing.T) {
	snap := fixture()
	const maxBytes = 4096 // très large pour 8 stations, serré pour 1519

	cases := map[string]any{
		"summary": Summarize(snap, refTime),
		"rank":    Rank(snap, MetricDocksAvailable, MaxRankLimit, false, refTime),
		"count":   Count(snap, FilterEmpty, MaxSampleSize, refTime),
		"find":    FindStations(snap, "gare de lyon", refTime),
	}

	for name, payload := range cases {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s : sérialisation : %v", name, err)
		}
		if len(b) > maxBytes {
			t.Errorf("%s : %d octets, au-dessus de la limite de %d",
				name, len(b), maxBytes)
		}
		t.Logf("%-8s %5d octets", name, len(b))
	}
}
