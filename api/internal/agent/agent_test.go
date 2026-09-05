package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go"
)

// stripReasoningContent est le correctif de compatibilité qui rend le projet
// utilisable avec Groq : les modèles raisonneurs renvoient un champ
// reasoning_content que le framework rejoue dans l'historique au tour suivant,
// et Groq répond 400 « property 'reasoning_content' is unsupported ».
//
// Il n'avait AUCUN test. C'est pourtant le genre de fonction qu'on supprime en
// refactorant parce qu'elle a l'air de ne rien faire : le premier tour continue
// de fonctionner, seules les conversations à plusieurs échanges cassent, et
// elles ne sont testées qu'en dernier.
func TestStripReasoningContentRetireLeChamp(t *testing.T) {
	assistant := &openaisdk.ChatCompletionAssistantMessageParam{}
	assistant.SetExtraFields(map[string]any{
		"reasoning_content": "l'utilisateur veut un comptage, appelons network_summary",
	})

	req := &openaisdk.ChatCompletionNewParams{
		Messages: []openaisdk.ChatCompletionMessageParamUnion{
			openaisdk.UserMessage("Combien de stations ?"),
			{OfAssistant: assistant},
		},
	}

	avant, _ := json.Marshal(req)
	if !strings.Contains(string(avant), "reasoning_content") {
		t.Fatal("le jeu d'essai ne contient pas le champ : le test ne prouve rien")
	}

	stripReasoningContent(context.Background(), req)

	apres, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}
	if strings.Contains(string(apres), "reasoning_content") {
		t.Errorf("le champ survit à l'envoi, Groq répondra 400 :\n%s", apres)
	}
}

// Le message utilisateur ne doit pas être touché : on retire un champ propre
// aux réponses du modèle, pas la question.
func TestStripReasoningContentEpargneLUtilisateur(t *testing.T) {
	req := &openaisdk.ChatCompletionNewParams{
		Messages: []openaisdk.ChatCompletionMessageParamUnion{
			openaisdk.UserMessage("Combien de vélos à Châtelet ?"),
		},
	}
	stripReasoningContent(context.Background(), req)

	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), "Châtelet") {
		t.Errorf("la question de l'utilisateur a été abîmée :\n%s", b)
	}
}

// Une requête nulle ou vide ne doit pas paniquer : la fonction est branchée
// comme rappel dans le framework, donc appelée sur des chemins qu'on ne
// maîtrise pas entièrement.
func TestStripReasoningContentSupporteLeVide(t *testing.T) {
	stripReasoningContent(context.Background(), nil)
	stripReasoningContent(context.Background(), &openaisdk.ChatCompletionNewParams{})
}

// Plusieurs messages assistant dans un même historique — le cas réel dès le
// troisième échange — doivent TOUS être nettoyés. N'en nettoyer qu'un laisserait
// la conversation casser un tour plus tard, ce qui est le pire des diagnostics.
func TestStripReasoningContentNettoieToutLHistorique(t *testing.T) {
	messages := []openaisdk.ChatCompletionMessageParamUnion{}
	for i := 0; i < 3; i++ {
		a := &openaisdk.ChatCompletionAssistantMessageParam{}
		a.SetExtraFields(map[string]any{"reasoning_content": "réflexion interne"})
		messages = append(messages,
			openaisdk.UserMessage("question"),
			openaisdk.ChatCompletionMessageParamUnion{OfAssistant: a})
	}
	req := &openaisdk.ChatCompletionNewParams{Messages: messages}

	stripReasoningContent(context.Background(), req)

	b, _ := json.Marshal(req)
	if n := strings.Count(string(b), "reasoning_content"); n != 0 {
		t.Errorf("%d occurrences restantes : le nettoyage s'arrête en chemin", n)
	}
}

// SessionKey et UserKey composent les clés de session. Elles sont triviales,
// mais elles portent le cloisonnement entre utilisateurs : si deux utilisateurs
// différents produisaient la même clé, chacun lirait les conversations de
// l'autre.
func TestClesDeSessionCloisonnent(t *testing.T) {
	a := SessionKey("alice", "conv-1")
	b := SessionKey("bob", "conv-1")
	if a == b {
		t.Fatal("deux utilisateurs, même conversation : clés identiques")
	}

	c := SessionKey("alice", "conv-2")
	if a == c {
		t.Fatal("deux conversations du même utilisateur : clés identiques")
	}

	// Le piège classique d'une concaténation naïve : « al » + « ice-conv » et
	// « alice » + « -conv » donneraient la même chaîne.
	x := SessionKey("al", "ice:conv-1")
	if x == a {
		t.Error("la composition des clés est ambiguë : deux couples distincts " +
			"produisent la même clé")
	}
}

// temperature doit rendre nil — et pas un pointeur vers 0 — quand la
// configuration est vide.
//
// La nuance décide du comportement réel : le champ du framework porte
// `omitempty`, donc nil ne part pas et 0 part. Or 0 est une température
// parfaitement valide, et c'est justement ce qu'un modèle raisonneur d'OpenAI
// rejette. Confondre les deux casserait l'échappatoire documentée.
func TestTemperatureVideNEnvoieRien(t *testing.T) {
	if got := temperature(""); got != nil {
		t.Errorf("température vide = %v, attendu nil : une valeur envoyée "+
			"casserait les modèles qui la refusent", *got)
	}

	// Zéro est une valeur, pas une absence.
	got := temperature("0")
	if got == nil {
		t.Fatal("température « 0 » rendue nil : zéro est une valeur valide, " +
			"c'est le VIDE qui signifie « ne rien envoyer »")
	}
	if *got != 0 {
		t.Errorf("température = %v, attendu 0", *got)
	}

	got = temperature("0.1")
	if got == nil || *got != 0.1 {
		t.Errorf("température « 0.1 » = %v, attendu 0.1", got)
	}

	// Illisible : config.Load l'a déjà refusée au démarrage, donc ce cas est
	// théoriquement inatteignable. On rend nil plutôt que de paniquer.
	if got := temperature("tiède"); got != nil {
		t.Errorf("température illisible = %v, attendu nil", *got)
	}
}
