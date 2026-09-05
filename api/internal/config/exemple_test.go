package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// La spécification demande « un .env.example listant TOUTES les variables attendues ».
//
// C'est une exigence littérale, et elle avait été manquée : API_ADDR,
// VELIB_INFORMATION_URL et VELIB_STATUS_URL étaient lues par le code sans
// apparaître dans le fichier d'exemple. Une variable qu'on ajoute au code et
// qu'on oublie ici est une variable que personne ne découvre avant de vouloir
// la changer en production.
//
// Ce test relit les deux et compare. Il échouera à la prochaine variable
// ajoutée sans être documentée, ce qu'aucune relecture ne garantit.
func TestEnvExempleListeToutesLesVariables(t *testing.T) {
	racine := racineDuDepot(t)

	// Ce que le code LIT réellement.
	source, err := os.ReadFile(filepath.Join(racine, "api", "internal", "config", "config.go"))
	if err != nil {
		t.Fatalf("lecture de config.go : %v", err)
	}
	// ⚠️ `env\w*\(` et non `env(?:Duration)?\(`.
	//
	// La version précédente énumérait les helpers connus : env et envDuration.
	// Le jour où un troisième est apparu — envPosee, pour distinguer « absente »
	// de « posée à vide » — sa variable a échappé au contrôle en silence, et ce
	// test est resté VERT sur un .env.example incomplet. C'est exactement le
	// défaut qu'il existe pour empêcher, à un niveau d'indirection près.
	//
	// On reconnaît maintenant toute fonction dont le nom commence par « env ».
	lues := map[string]bool{}
	for _, m := range regexp.MustCompile(`env\w*\("([A-Z_][A-Z0-9_]*)"`).
		FindAllSubmatch(source, -1) {
		lues[string(m[1])] = true
	}
	if len(lues) < 5 {
		t.Fatalf("seulement %d variables trouvées dans config.go : "+
			"l'expression ne reconnaît plus la forme des appels", len(lues))
	}

	// Ce que le fichier d'exemple DOCUMENTE, ligne active ou commentée : une
	// variable avec un défaut utilisable a le droit d'être commentée, elle doit
	// juste être mentionnée.
	exemple, err := os.ReadFile(filepath.Join(racine, ".env.example"))
	if err != nil {
		t.Fatalf("lecture de .env.example : %v", err)
	}
	texte := string(exemple)

	var manquantes []string
	for nom := range lues {
		if !strings.Contains(texte, nom) {
			manquantes = append(manquantes, nom)
		}
	}
	sort.Strings(manquantes)
	if len(manquantes) > 0 {
		t.Errorf("%d variable(s) lue(s) par le code et absente(s) de "+
			".env.example : %s\n\nLa spécification demande que le fichier les liste TOUTES.",
			len(manquantes), strings.Join(manquantes, ", "))
	}

	// Le sens inverse compte aussi : une variable documentée que plus personne
	// ne lit envoie le lecteur configurer quelque chose sans effet.
	//
	// On ignore celles que docker-compose consomme lui-même, elles n'atteignent
	// jamais le code Go.
	consommeesParCompose := map[string]bool{
		"POSTGRES_PORT": true, "API_PORT": true, "WEB_PORT": true,
		"POSTGRES_USER": true, "POSTGRES_PASSWORD": true, "POSTGRES_DB": true,
	}
	var orphelines []string
	for _, m := range regexp.MustCompile(`(?m)^#?\s*([A-Z_][A-Z0-9_]*)=`).
		FindAllStringSubmatch(texte, -1) {
		nom := m[1]
		if !lues[nom] && !consommeesParCompose[nom] {
			orphelines = append(orphelines, nom)
		}
	}
	sort.Strings(orphelines)
	if len(orphelines) > 0 {
		t.Errorf("%d variable(s) documentée(s) que le code ne lit plus : %s\n\n"+
			"Elles envoient le lecteur configurer quelque chose sans effet.",
			len(orphelines), strings.Join(orphelines, ", "))
	}

	if !t.Failed() {
		t.Logf("%d variables lues par le code, toutes documentées", len(lues))
	}
}

// racineDuDepot remonte jusqu'au dossier qui contient .env.example.
func racineDuDepot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("répertoire courant : %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".env.example")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip(".env.example hors de portée depuis ce répertoire")
	return ""
}
