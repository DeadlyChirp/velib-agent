package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// L'arrêt propre repose sur un accord entre DEUX fichiers qui ne se connaissent
// pas : main.go accorde un délai aux conversations en cours, et docker-compose
// décide combien de temps Docker attend avant d'envoyer SIGKILL.
//
// Si le second est plus court que le premier, le code croit finir proprement et
// se fait tuer en chemin. C'était le cas : 25 secondes demandées, 10 accordées
// par le défaut de Docker.
//
// Le défaut ne se voyait que sur les tours longs — plus de dix secondes, mesurés
// jusqu'à 26 s sur un modèle raisonneur. Invisible en développement, où l'on
// redéploie rarement au milieu d'une conversation.
//
// Ce test relit les deux fichiers et vérifie qu'ils restent d'accord.

func TestDelaiDeGraceDockerCouvreLArretDuCode(t *testing.T) {
	racine, ok := racineDuDepot()
	if !ok {
		// Le conteneur de test ne monte que api/ : docker-compose.yml est hors
		// de portée. Un test qui ne PEUT PAS s'exécuter s'abstient, il n'échoue
		// pas — sinon on apprend à ignorer un rouge qui ne veut rien dire.
		// La CI, elle, récupère tout le dépôt et l'exécute vraiment.
		t.Skip("docker-compose.yml hors de portée depuis ce répertoire")
	}

	// Ce que le code s'accorde.
	source, err := os.ReadFile(filepath.Join(racine, "api", "cmd", "api", "main.go"))
	if err != nil {
		t.Fatalf("lecture de main.go : %v", err)
	}
	// Ancré sur le NOM de la variable et pas seulement sur la forme de l'appel :
	// main.go contient un autre WithTimeout, celui du préchauffage du cache, et
	// une expression trop large capturait le mauvais délai — un test qui mesure
	// autre chose que ce qu'il annonce.
	m := regexp.MustCompile(`shutdownCtx,\s*\w+\s*:?=\s*context\.WithTimeout\([^,]+,\s*(\d+)\s*\*\s*time\.Second\)`).
		FindSubmatch(source)
	if m == nil {
		t.Fatal("délai d'arrêt introuvable dans main.go — si la forme a changé, " +
			"mettre ce test à jour plutôt que de le supprimer")
	}
	delaiCode, _ := strconv.Atoi(string(m[1]))

	// Ce que Docker accorde.
	compose, err := os.ReadFile(filepath.Join(racine, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("lecture de docker-compose.yml : %v", err)
	}
	g := regexp.MustCompile(`stop_grace_period:\s*(\d+)s`).FindSubmatch(compose)
	if g == nil {
		t.Fatal("stop_grace_period absent de docker-compose.yml : Docker enverra " +
			"SIGKILL au bout de 10 s, quel que soit le délai demandé par le code")
	}
	delaiDocker, _ := strconv.Atoi(string(g[1]))

	t.Logf("code : %d s, Docker : %d s", delaiCode, delaiDocker)

	if delaiDocker <= delaiCode {
		t.Errorf("Docker accorde %d s alors que le code en demande %d : les "+
			"conversations en cours seront coupées", delaiDocker, delaiCode)
	}
	// De la marge pour journaliser l'arrêt après la fermeture du serveur.
	if delaiDocker-delaiCode < 3 {
		t.Errorf("seulement %d s de marge entre les deux : trop peu pour "+
			"journaliser l'arrêt", delaiDocker-delaiCode)
	}

	// Un délai d'arrêt doit aussi couvrir un tour d'agent réel. Mesuré jusqu'à
	// 26 s sur un modèle raisonneur : en dessous, un redéploiement coupe une
	// réponse que l'utilisateur était en train de lire.
	const tourLeMoinsFavorableMesure = 26 * time.Second
	if time.Duration(delaiDocker)*time.Second < tourLeMoinsFavorableMesure {
		t.Errorf("délai de grâce de %d s alors qu'un tour a été mesuré à %v : "+
			"une conversation longue sera coupée", delaiDocker, tourLeMoinsFavorableMesure)
	}
}

// racineDuDepot remonte depuis le paquet jusqu'au dossier qui contient
// docker-compose.yml. Un chemin relatif en dur casserait au premier
// déplacement du fichier.
//
// Rend false plutôt que d'échouer : le fichier peut légitimement être hors de
// portée, par exemple quand les tests tournent dans un conteneur qui ne monte
// que api/.
func racineDuDepot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}
