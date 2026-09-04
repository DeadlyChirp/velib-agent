package tools

import (
	"slices"
	"testing"
)

// Le generateur de schema marque TOUT champ obligatoire sauf s'il porte
// « omitempty » — la directive jsonschema « required » ne fait qu'ajouter, elle
// ne retranche rien. Un parametre a valeur par defaut laisse donc une trace
// « required » dans le schema, en contradiction avec sa propre description.
//
// OpenAI tolere et laisse passer. Groq applique le schema a la lettre et rejette
// l'appel en 400 « missing properties: 'ascending' », alors que le modele avait
// raison d'omettre le champ. C'etait invisible jusqu'a ce qu'on change de
// fournisseur.
//
// Ce test fige la liste attendue : ajouter un parametre optionnel sans
// « omitempty » le fait echouer ici, pas en production chez un fournisseur
// stricte.
func TestSchemaRequiredFields(t *testing.T) {
	attendu := map[string][]string{
		"network_summary": {},
		"find_station":    {"name"},
		"rank_stations":   {"metric"},
		"count_stations":  {"filter"},
	}

	for _, tl := range NewRegistry(nil, nil, nil).All() {
		d := tl.Declaration()
		veut, connu := attendu[d.Name]
		if !connu {
			t.Errorf("outil %q absent de la liste attendue — completer ce test", d.Name)
			continue
		}
		var obtenu []string
		if d.InputSchema != nil {
			obtenu = d.InputSchema.Required
		}
		slices.Sort(obtenu)
		slices.Sort(veut)
		if !slices.Equal(obtenu, veut) {
			t.Errorf("%s : champs obligatoires = %v, attendu %v\n"+
				"  un parametre a valeur par defaut doit porter `json:\"...,omitempty\"`",
				d.Name, obtenu, veut)
		}
	}
}
