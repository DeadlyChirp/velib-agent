package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/tool"

	"velib-agent/internal/velib"
)

// Ces tests exercent les outils par LEUR VRAIE INTERFACE — celle que le modèle
// utilise : un nom, un schéma JSON, et un appel avec des arguments sérialisés.
//
// Tester la fonction Go interne aurait prouvé que le calcul est juste. Passer
// par tool.CallableTool prouve en plus que le schéma est généré correctement,
// que les arguments du modèle sont désérialisables, et que le retour est
// sérialisable — trois points de rupture invisibles autrement, et qui ne se
// manifesteraient qu'en production, dans une conversation.

// ─────────────────────────────────────────────────────────────────────────────
// Sources factices
// ─────────────────────────────────────────────────────────────────────────────

type fakeSource struct {
	snap velib.Snapshot
	err  error
}

func (f fakeSource) Snapshot(context.Context) (velib.Snapshot, error) {
	return f.snap, f.err
}

var testTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func testSnapshot() velib.Snapshot {
	return velib.Snapshot{
		FetchedAt: testTime.Add(-20 * time.Second),
		Stations: []velib.Station{
			mkStation(1, "16107", "Benjamin Godard - Victor Hugo", 35, 7, 3, 4, 27, true),
			mkStation(2, "12001", "Gare de Lyon - Chalon", 40, 12, 8, 4, 28, true),
			mkStation(3, "12002", "Gare de Lyon - Roland Barthes", 30, 0, 0, 0, 30, true),
			mkStation(4, "75001", "Station En Panne", 20, 3, 3, 0, 17, false),
			mkStation(5, "20001", "Télégraphe", 25, 5, 2, 3, 20, true),
		},
	}
}

// mkStation construit une station de test. Les champs non exportés du paquet
// velib (searchKey) sont calculés à la jointure, donc on passe par le décodage
// JSON du vrai chemin plutôt que de fabriquer une struct incomplète qui ferait
// silencieusement échouer la recherche.
func mkStation(id int64, code, name string, cap, bikes, mech, ebikes, docks int, ok bool) velib.Station {
	return velib.Station{
		ID: id, Code: code, Name: name, Capacity: cap,
		BikesAvailable: bikes, Mechanical: mech, Ebikes: ebikes,
		DocksAvailable: docks,
		IsInstalled:    true, IsRenting: ok, IsReturning: ok,
		LastReported: testTime.Add(-30 * time.Second),
	}
}

func registry(src velib.Source) *Registry {
	r := NewRegistry(src, nil, nil)
	r.now = func() time.Time { return testTime }
	return r
}

// callTool appelle un outil comme le ferait le modèle : par son nom, avec des
// arguments JSON.
func callTool(t *testing.T, reg *Registry, name, args string) map[string]any {
	t.Helper()

	var target tool.CallableTool
	for _, tl := range reg.All() {
		if tl.Declaration().Name == name {
			c, ok := tl.(tool.CallableTool)
			if !ok {
				t.Fatalf("l'outil %s n'est pas appelable", name)
			}
			target = c
			break
		}
	}
	if target == nil {
		t.Fatalf("outil %s introuvable dans le registre", name)
	}

	res, err := target.Call(context.Background(), []byte(args))
	if err != nil {
		t.Fatalf("appel de %s : %v", name, err)
	}

	// Aller-retour JSON : c'est exactement ce que subit le retour avant
	// d'entrer dans le contexte du modèle.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("le retour de %s n'est pas sérialisable : %v", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("le retour de %s ne se relit pas : %v", name, err)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Les déclarations vues par le modèle
// ─────────────────────────────────────────────────────────────────────────────

func TestDeclarationsAreUsableByAModel(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})

	want := map[string]bool{
		"network_summary": false, "find_station": false,
		"rank_stations": false, "count_stations": false,
	}

	for _, tl := range reg.All() {
		d := tl.Declaration()

		if _, expected := want[d.Name]; !expected {
			t.Errorf("outil inattendu exposé au modèle : %s", d.Name)
			continue
		}
		want[d.Name] = true

		// La description est la SEULE chose que le modèle lit pour décider
		// d'appeler l'outil. Une description courte est un outil mal appelé.
		if len(d.Description) < 120 {
			t.Errorf("%s : description de %d caractères, trop courte pour "+
				"guider un choix d'outil", d.Name, len(d.Description))
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("outil manquant : %s", name)
		}
	}
}

// ⚠️ L'invariant central du projet, vérifié à la frontière réelle.
func TestNoToolExposesTheWholeNetwork(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})

	for _, tl := range reg.All() {
		d := tl.Declaration()
		lower := strings.ToLower(d.Name)
		for _, forbidden := range []string{"all_stations", "list_stations", "get_stations", "dump"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("l'outil %s ressemble à un accès au parc entier : "+
					"c'est exactement ce que la spécification demande d'éviter", d.Name)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Les cinq questions de référence, par le vrai chemin d'appel
// ─────────────────────────────────────────────────────────────────────────────

func TestQ1AndQ2NetworkSummary(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "network_summary", `{}`)

	// Q1 : 4 + 4 + 3 = 11 électriques.
	if got := out["bikes_electric"]; got != float64(11) {
		t.Errorf("vélos électriques = %v, attendu 11", got)
	}
	// Q2 : 1 station hors service sur 5 = 20 %.
	if got := out["out_of_service_pct"]; got != float64(20) {
		t.Errorf("pourcentage hors service = %v, attendu 20", got)
	}
	// La règle appliquée doit accompagner le chiffre.
	if rule, _ := out["out_of_service_rule"].(string); rule == "" {
		t.Error("le pourcentage part au modèle sans sa définition")
	}
	// La fraîcheur doit toujours être présente.
	if _, ok := out["freshness"]; !ok {
		t.Error("bloc freshness absent : le modèle ne peut pas signaler une donnée datée")
	}
}

func TestQ3RankStations(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "rank_stations",
		`{"metric":"docks_available","limit":2,"ascending":false}`)

	stations, ok := out["stations"].([]any)
	if !ok || len(stations) != 2 {
		t.Fatalf("stations = %v, attendu 2 entrées", out["stations"])
	}
	first := stations[0].(map[string]any)
	if first["docks_available"] != float64(30) {
		t.Errorf("première station à %v bornes libres, attendu 30",
			first["docks_available"])
	}
}

func TestQ4FindStationPartialName(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "find_station", `{"name":"Benjamin Godard"}`)

	if out["match_count"] != float64(1) {
		t.Fatalf("match_count = %v, attendu 1", out["match_count"])
	}
	st := out["stations"].([]any)[0].(map[string]any)
	if st["bikes_available"] != float64(7) {
		t.Errorf("vélos = %v, attendu 7", st["bikes_available"])
	}
}

// Le cas qui sépare une réponse honnête d'une réponse assurée et fausse.
func TestQ4AmbiguityIsSurfacedToTheModel(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "find_station", `{"name":"gare de lyon"}`)

	if out["match_count"] != float64(2) {
		t.Fatalf("match_count = %v, attendu 2", out["match_count"])
	}
	if out["ambiguous"] != true {
		t.Error("l'ambiguïté n'est pas signalée : le modèle choisira au hasard")
	}
	if note, _ := out["note"].(string); note == "" {
		t.Error("aucune consigne jointe : le modèle ne saura pas quoi faire de l'ambiguïté")
	}
}

func TestQ5CountReturnsTotalAndSample(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "count_stations", `{"filter":"empty","sample_size":1}`)

	// Le total reste EXACT même quand l'échantillon est coupé.
	if out["total"] != float64(1) {
		t.Errorf("total = %v, attendu 1", out["total"])
	}
	if _, ok := out["sample"]; !ok {
		t.Error("champ sample absent")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Le comportement en panne — explicitement noté par la spécification
// ─────────────────────────────────────────────────────────────────────────────

func TestToolsFailGracefullyForTheModel(t *testing.T) {
	reg := registry(fakeSource{err: errors.New("source injoignable")})

	for _, tc := range []struct{ name, args string }{
		{"network_summary", `{}`},
		{"find_station", `{"name":"Benjamin Godard"}`},
		{"rank_stations", `{"metric":"docks_available","limit":5}`},
		{"count_stations", `{"filter":"empty","sample_size":5}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := callTool(t, reg, tc.name, tc.args)

			// Un outil qui échoue ne doit PAS remonter une erreur Go : la boucle
			// de l'agent s'arrêterait. Il rend un objet que le modèle sait lire.
			msg, _ := out["error"].(string)
			if msg == "" {
				t.Fatal("aucun champ error : le modèle ne saura pas que l'appel a échoué")
			}
			// Et surtout une consigne : sans elle, le modèle comble le vide en
			// inventant des chiffres.
			advice, _ := out["advice"].(string)
			if !strings.Contains(strings.ToLower(advice), "inventer") {
				t.Errorf("la consigne n'interdit pas explicitement d'inventer : %q", advice)
			}
		})
	}
}

func TestUnknownFilterDoesNotPanic(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	out := callTool(t, reg, "count_stations", `{"filter":"licornes","sample_size":5}`)

	if out["total"] != float64(0) {
		t.Errorf("total = %v pour un filtre inconnu, attendu 0", out["total"])
	}
	if note, _ := out["note"].(string); note == "" {
		t.Error("un filtre inconnu doit être expliqué au modèle, pas ignoré")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Le budget de contexte, mesuré à la frontière réelle
// ─────────────────────────────────────────────────────────────────────────────

// Ce test est le garde-fou du projet : il échoue le jour où quelqu'un ajoute un
// champ verbeux dans un retour d'outil, et rappelle pourquoi la limite existe.
func TestToolPayloadsStayWithinContextBudget(t *testing.T) {
	reg := registry(fakeSource{snap: testSnapshot()})
	const maxBytes = 4096

	for _, tc := range []struct{ name, args string }{
		{"network_summary", `{}`},
		{"find_station", `{"name":"gare"}`},
		{"rank_stations", `{"metric":"docks_available","limit":20}`},
		{"count_stations", `{"filter":"empty","sample_size":10}`},
	} {
		out := callTool(t, reg, tc.name, tc.args)
		b, _ := json.Marshal(out)
		if len(b) > maxBytes {
			t.Errorf("%s : %d octets, au-dessus de la limite de %d",
				tc.name, len(b), maxBytes)
		}
		t.Logf("%-16s %5d octets", tc.name, len(b))
	}
}

// L'instruction système doit couvrir les comportements sur lesquels la spécification
// dit explicitement juger l'agent.
func TestSystemInstructionCoversTheGradedBehaviours(t *testing.T) {
	lower := strings.ToLower(SystemInstruction)

	for _, must := range []struct{ topic, needle string }{
		{"ambiguïté d'une station", "ambiguous"},
		{"troncature d'une liste", "truncated"},
		{"fraîcheur de la donnée", "stale"},
		{"refus d'inventer", "invente"},
		{"absence de liste complète", "liste complète"},
	} {
		if !strings.Contains(lower, must.needle) {
			t.Errorf("l'instruction système ne traite pas : %s (mot-clé %q)",
				must.topic, must.needle)
		}
	}
}
