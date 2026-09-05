//go:build integration

// Tests en boîte noire contre la pile RÉELLE : routes, PostgreSQL, isolation.
//
// Exclus du build par défaut, comme les tests live : ils exigent un
// « docker compose up » et échoueraient en CI sans conteneurs, ce qui rendrait
// la suite mensongère.
//
//	docker compose up -d
//	go test -tags=integration ./internal/httpapi/ -v
//
// Ils couvrent ce que les tests unitaires ne peuvent pas : le cycle de vie
// complet d'une conversation, la séparation entre utilisateurs, et la forme
// exacte du flux SSE telle qu'un navigateur la reçoit.
package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func baseURL() string {
	if u := os.Getenv("API_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// appel envoie une requête et rend le statut et le corps.
func appel(t *testing.T, methode, chemin, utilisateur, corps string) (int, []byte) {
	t.Helper()
	var body io.Reader
	if corps != "" {
		body = strings.NewReader(corps)
	}
	req, err := http.NewRequest(methode, baseURL()+chemin, body)
	if err != nil {
		t.Fatalf("construction de la requête : %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if utilisateur != "" {
		req.Header.Set("X-User-ID", utilisateur)
	}
	rsp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s : %v (la pile tourne-t-elle ?)", methode, chemin, err)
	}
	defer rsp.Body.Close()
	b, _ := io.ReadAll(rsp.Body)
	return rsp.StatusCode, b
}

func idDe(t *testing.T, corps []byte) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(corps, &v); err != nil || v.ID == "" {
		t.Fatalf("pas d'identifiant dans la réponse : %s", corps)
	}
	return v.ID
}

// Cycle de vie complet, tel qu'un client l'enchaîne.
func TestCycleDeVieConversation(t *testing.T) {
	moi := fmt.Sprintf("test-cycle-%d", time.Now().UnixNano())

	st, b := appel(t, "POST", "/api/conversations", moi, "")
	if st != http.StatusCreated {
		t.Fatalf("création : statut %d, corps %s", st, b)
	}
	id := idDe(t, b)

	if st, b = appel(t, "GET", "/api/conversations/"+id, moi, ""); st != http.StatusOK {
		t.Fatalf("relecture : statut %d, corps %s", st, b)
	}

	st, b = appel(t, "GET", "/api/conversations", moi, "")
	if st != http.StatusOK || !strings.Contains(string(b), id) {
		t.Fatalf("la liste ne contient pas %s : statut %d, corps %s", id, st, b)
	}

	if st, b = appel(t, "DELETE", "/api/conversations/"+id, moi, ""); st != http.StatusNoContent {
		t.Fatalf("suppression : statut %d, corps %s", st, b)
	}

	// Une conversation supprimée ne doit plus être lisible. Un 200 ici
	// signifierait une suppression qui ne supprime pas — panne silencieuse.
	if st, _ = appel(t, "GET", "/api/conversations/"+id, moi, ""); st != http.StatusNotFound {
		t.Errorf("après suppression : statut %d, attendu 404", st)
	}
}

// Le point que le README signale comme à durcir avant toute mise en ligne :
// l'en-tête X-User-ID isole les conversations, mais rien ne le VÉRIFIAIT. Ce
// test fige au moins la séparation elle-même — si elle casse un jour, on
// l'apprend ici et pas par un utilisateur lisant les conversations d'un autre.
func TestIsolationEntreUtilisateurs(t *testing.T) {
	alice := fmt.Sprintf("test-alice-%d", time.Now().UnixNano())
	bob := fmt.Sprintf("test-bob-%d", time.Now().UnixNano())

	_, b := appel(t, "POST", "/api/conversations", alice, "")
	idAlice := idDe(t, b)
	t.Cleanup(func() { appel(t, "DELETE", "/api/conversations/"+idAlice, alice, "") })

	if st, corps := appel(t, "GET", "/api/conversations", bob, ""); strings.Contains(string(corps), idAlice) {
		t.Fatalf("FUITE : Bob voit la conversation d'Alice (statut %d)\n%s", st, corps)
	}
	if st, _ := appel(t, "GET", "/api/conversations/"+idAlice, bob, ""); st == http.StatusOK {
		t.Fatal("FUITE : Bob lit la conversation d'Alice par son identifiant")
	}
}

// Entrées invalides : chacune doit produire un 4xx explicite, jamais un 500 ni
// un 200 silencieux.
func TestEntreesInvalides(t *testing.T) {
	moi := fmt.Sprintf("test-invalide-%d", time.Now().UnixNano())
	_, b := appel(t, "POST", "/api/conversations", moi, "")
	id := idDe(t, b)
	t.Cleanup(func() { appel(t, "DELETE", "/api/conversations/"+id, moi, "") })

	cas := []struct {
		nom, methode, chemin, corps string
		attendu                     int
	}{
		{"message vide", "POST", "/api/conversations/" + id + "/messages",
			`{"message":"   "}`, http.StatusBadRequest},
		{"JSON cassé", "POST", "/api/conversations/" + id + "/messages",
			`{pas du json`, http.StatusBadRequest},
		{"conversation inconnue", "GET", "/api/conversations/inexistante-0000",
			"", http.StatusNotFound},
		{"méthode non prévue", "PUT", "/api/conversations",
			"", http.StatusMethodNotAllowed},
		// Bornes trouvées par l'audit de comportements : chacune produisait un
		// 500 ou passait silencieusement avant d'être fermée.
		{"question trop longue", "POST", "/api/conversations/" + id + "/messages",
			`{"message":"` + strings.Repeat("a", 3000) + `"}`, http.StatusBadRequest},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			st, corps := appel(t, c.methode, c.chemin, moi, c.corps)
			if st != c.attendu {
				t.Errorf("statut %d, attendu %d — corps : %s", st, c.attendu, corps)
			}
			if st >= 500 {
				t.Errorf("une entrée invalide ne doit jamais produire un %d", st)
			}
		})
	}
}

// Une identité applicative démesurée doit produire un 400 et non un 500.
//
// Trouvé par l'audit : l'en-tête partait tel quel dans une colonne varchar(255)
// et la base refusait l'insertion. L'appelant recevait « erreur interne » pour
// une requête que LUI pouvait corriger.
func TestIdentiteDemesureeEstRefuseeProprement(t *testing.T) {
	st, corps := appel(t, "GET", "/api/conversations", strings.Repeat("u", 500), "")
	if st != http.StatusBadRequest {
		t.Errorf("statut %d, attendu 400 — corps : %s", st, corps)
	}
	if st >= 500 {
		t.Error("une entrée client invalide ne doit jamais réveiller une astreinte")
	}
}

// Santé et métriques : deux routes qu'une supervision interroge en boucle.
func TestSanteEtMetriques(t *testing.T) {
	st, b := appel(t, "GET", "/api/health", "", "")
	if st != http.StatusOK {
		t.Fatalf("santé : statut %d, corps %s", st, b)
	}
	var sante struct {
		Status   string `json:"status"`
		Postgres string `json:"postgres"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(b, &sante); err != nil {
		t.Fatalf("santé non décodable : %s", b)
	}
	if sante.Status != "ok" || sante.Postgres != "ok" {
		t.Errorf("santé dégradée : %+v", sante)
	}
	if sante.Model == "" {
		t.Error("la santé n'annonce aucun modèle")
	}

	if st, b = appel(t, "GET", "/api/metrics", "", ""); st != http.StatusOK {
		t.Fatalf("métriques : statut %d, corps %s", st, b)
	}
	var m struct {
		Cache struct {
			Stations int `json:"stations"`
		} `json:"cache"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("métriques non décodables : %s", b)
	}
	if m.Cache.Stations == 0 {
		t.Error("les métriques annoncent zéro station : le cache n'a pas chargé")
	}
}

// La forme du flux SSE, telle qu'un navigateur la reçoit. Un seul appel au
// modèle : ces tests tournent sur un palier gratuit limité en jetons.
func TestFluxSSE(t *testing.T) {
	moi := fmt.Sprintf("test-sse-%d", time.Now().UnixNano())
	_, b := appel(t, "POST", "/api/conversations", moi, "")
	id := idDe(t, b)
	t.Cleanup(func() { appel(t, "DELETE", "/api/conversations/"+id, moi, "") })

	req, _ := http.NewRequest("POST", baseURL()+"/api/conversations/"+id+"/messages",
		bytes.NewReader([]byte(`{"message":"Combien de stations au total ?"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", moi)

	rsp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("appel : %v", err)
	}
	defer rsp.Body.Close()

	if ct := rsp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, attendu text/event-stream", ct)
	}

	corps, _ := io.ReadAll(rsp.Body)
	texte := string(corps)
	if !strings.Contains(texte, "data: ") {
		t.Fatalf("aucune ligne « data: » dans le flux :\n%s", texte)
	}

	// Chaque ligne data: doit porter un JSON valide avec un champ type : un
	// front qui reçoit du JSON cassé ne peut rien afficher d'utile.
	var vuDone, vuErreur bool
	for _, ligne := range strings.Split(texte, "\n") {
		if !strings.HasPrefix(ligne, "data: ") {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(ligne, "data: ")), &ev); err != nil {
			t.Errorf("ligne SSE non décodable : %s", ligne)
			continue
		}
		if ev.Type == "" {
			t.Errorf("événement SSE sans champ type : %s", ligne)
		}
		switch ev.Type {
		case "done":
			vuDone = true
		case "error":
			vuErreur = true
			// Un flux qui finit en erreur est LÉGITIME ici : le quota du palier
			// gratuit s'épuise, et la CI tourne avec une clé factice. Ce qui
			// n'est pas négociable, c'est que le message dise quoi faire.
			//
			// On teste donc l'invariant utile — « pas le message générique » —
			// plutôt que la présence d'un mot précis, qui ne ferait que figer
			// une formulation.
			if strings.HasPrefix(ev.Error, "Une erreur est survenue") {
				t.Errorf("message générique renvoyé à l'utilisateur, sans action possible : %q", ev.Error)
			}
			t.Logf("flux terminé en erreur (attendu sans clé valide ou quota épuisé) : %s", ev.Error)
		}
	}
	if !vuDone && !vuErreur {
		t.Error("le flux ne se termine ni par done ni par error")
	}
}

// message_count doit compter le tableau messages, pas les événements du
// framework.
//
// Les deux différaient : les événements incluent les appels d'outils et leurs
// réponses, si bien qu'un simple aller-retour rendait « 4 messages » à côté
// d'un tableau qui en contenait 2. Et la vue « liste » n'ayant pas les
// événements — elle charge les sessions en mode métadonnées seules, par choix
// de performance — le champ y sortait à 0 pour tout le monde : pas « aucun
// message », mais « je n'en sais rien », écrit comme un fait.
//
// Trouvé en écrivant le test de persistance, qui affichait les deux nombres
// côte à côte. Aucun test ne les avait jamais comparés.
func TestCompteDeMessagesCorrespondAuTableau(t *testing.T) {
	moi := fmt.Sprintf("test-compte-%d", time.Now().UnixNano())

	st, b := appel(t, "POST", "/api/conversations", moi, "")
	if st != http.StatusCreated {
		t.Fatalf("création : statut %d, corps %s", st, b)
	}
	id := idDe(t, b)
	defer appel(t, "DELETE", "/api/conversations/"+id, moi, "")

	// ⚠️ Il FAUT un message, sinon ce test ne teste rien.
	//
	// La première version relisait la conversation à peine créée : le compte et
	// le tableau valaient tous deux zéro, l'assertion comparait 0 à 0, et le
	// défaut qu'elle documente serait repassé au vert sans être corrigé.
	//
	// Le modèle peut échouer ici — la CI tourne avec une clé factice — et ça
	// n'a pas d'importance : la QUESTION est persistée dans tous les cas, ce qui
	// suffit à rendre les deux valeurs non nulles et l'assertion mordante.
	appel(t, "POST", "/api/conversations/"+id+"/messages", moi,
		`{"message":"Combien de stations au total ?"}`)

	st, b = appel(t, "GET", "/api/conversations/"+id, moi, "")
	if st != http.StatusOK {
		t.Fatalf("relecture : statut %d, corps %s", st, b)
	}

	var detail struct {
		MessageCount *int `json:"message_count"`
		Messages     []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &detail); err != nil {
		t.Fatalf("détail illisible : %v — %s", err, b)
	}
	if detail.MessageCount == nil {
		t.Fatal("message_count absent de la vue détail, qui est la seule à " +
			"pouvoir le calculer")
	}
	// Garde-fou du garde-fou : si la conversation est vide, l'assertion
	// suivante compare 0 à 0 et ne prouve rien. On refuse ce cas plutôt que de
	// rendre un vert vide.
	if len(detail.Messages) == 0 {
		t.Fatal("aucun message persisté : l'assertion suivante comparerait " +
			"0 à 0 et ne testerait rien")
	}
	if *detail.MessageCount != len(detail.Messages) {
		t.Errorf("message_count = %d pour %d message(s) dans le tableau : "+
			"un champ nommé message_count posé à côté d'un tableau messages "+
			"doit compter ce tableau", *detail.MessageCount, len(detail.Messages))
	}

	// La liste ne peut pas compter : elle ne doit donc RIEN annoncer plutôt
	// qu'annoncer zéro.
	st, b = appel(t, "GET", "/api/conversations", moi, "")
	if st != http.StatusOK {
		t.Fatalf("liste : statut %d, corps %s", st, b)
	}
	if strings.Contains(string(b), "message_count") {
		t.Errorf("la vue liste expose message_count alors qu'elle ne charge "+
			"pas les événements : la valeur serait fausse. Corps : %s", b)
	}
}
