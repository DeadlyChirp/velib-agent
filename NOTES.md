# Journal de bord

Ce fichier n'est pas de la documentation. C'est le journal de ce que j'ai
mesuré, vérifié, tenté et corrigé pendant le test — y compris ce qui a échoué.

Il sert à deux choses :

1. me permettre de **défendre chaque choix** pendant les 45 minutes de
   soutenance, avec les chiffres qui l'ont motivé plutôt qu'une intuition ;
2. répondre précisément à la question posée par la spécification — *« ce qui a été
   généré, ce que tu as relu et ce que tu as corrigé »* — au lieu d'un vague
   « j'ai tout relu ».

---

## 1. Vérification des dépendances, avant d'écrire une ligne

La spécification impose `trpc-agent-go`. Avant de coder, j'ai cloné le dépôt et lu le
code plutôt que de me fier à ma mémoire ou à une réponse de modèle.

| Point vérifié | Source | Résultat |
|---|---|---|
| Chemin du module | `go.mod` racine | `trpc.group/trpc-go/trpc-agent-go` — **pas** l'URL GitHub. C'est un *vanity import path* ; écrire `github.com/trpc-group/...` ne compile pas. |
| Signature du Runner | `runner/runner.go:223` | `Run(ctx, userID, sessionID, message model.Message, runOpts ...agent.RunOption) (<-chan *event.Event, error)` — renvoie un **channel**, d'où le streaming natif. |
| Constructeur d'outil | `tool/function/function_tool.go:175` | `NewFunctionTool[I, O any](fn func(context.Context, I) (O, error), opts ...Option)` — générique, le schéma JSON vient des tags de la struct d'entrée. |
| Persistance des sessions | `session/postgres/` | **Un service PostgreSQL existe déjà dans le framework.** Sous-module Go séparé : `trpc.group/trpc-go/trpc-agent-go/session/postgres`, constructeur `NewService(...ServiceOpt) (*Service, error)`. |
| Interface Session | `session/session.go:1111` | `CreateSession` / `GetSession` / `ListSessions` / `DeleteSession` — soit **exactement** les quatre opérations de conversation demandées par la spécification. |

**Conséquence directe sur l'architecture.** La spécification demande que « les
conversations et leur historique vivent en base et survivent au redémarrage ».
J'avais prévu d'écrire ma propre table de messages. La lecture du code a montré
que le framework le fait déjà, avec le même modèle de clés
(`session.Key{AppName, UserID, SessionID}`). J'ai donc **supprimé ma couche de
persistance maison** : moins de code à maintenir, et le comportement de reprise
de session est celui que le framework attend, pas une réimplémentation
approximative.

> À dire en soutenance : *« j'ai lu le code du framework avant d'écrire le mien,
> et ça m'a évité d'écrire une couche entière qui existait déjà. »*

---

## 2. Mesures sur les données réelles

Aucun chiffre de ce projet n'est supposé. Tout est mesuré, et **re-mesuré**,
parce que le parc bouge.

### Deux relevés, à quelques heures d'intervalle

| Mesure | 27/08 ~10 h | 27/08 ~11 h | Variation |
|---|---|---|---|
| Stations | 1 519 | 1 519 | stable |
| Vélos disponibles | 20 478 | 18 499 | −10 % |
| dont électriques | 8 500 | 7 128 | −16 % |
| Hors service | 16 (1,05 %) | 22 (1,45 %) | +38 % |
| **Stations vides** | **49 (3,2 %)** | **115 (7,6 %)** | **×2,3** |
| Capacité nulle | 3 | 4 | +1 |
| `bikes+docks > capacity` | 8 | 15 | ×1,9 |
| Remontée > 1 h | 17 | **441** | **×26** |

### Ce que cette variation démontre

**C'est l'argument le plus fort du projet.** Un outil qui renverrait « toutes
les stations vides » aurait produit 49 lignes le matin et 115 une heure plus
tard. Rien dans le code n'aurait changé ; seule la météo. Un dimanche soir de
pluie, ce serait plusieurs centaines.

Une taille de sortie qui dépend de la météo n'est pas une conception, c'est un
pari. La forme `{total, sample, truncated}` garde une taille **constante** quoi
qu'il arrive, et répond mieux à l'intention réelle de l'utilisateur, qui veut
d'abord savoir *combien*.

Le passage de 17 à 441 stations muettes depuis plus d'une heure est du même
ordre : si le seuil de signalement de fraîcheur avait été calé sur le seul
relevé du matin, il aurait été absurde une heure plus tard.

### Les pièges, un par un

| # | Piège | Constat mesuré | Traitement |
|---|---|---|---|
| 1 | **Jointure obligatoire** | Le nom est dans `station_information`, la disponibilité dans `station_status`. Aucune question intéressante n'est répondable avec un seul fichier. | Jointure sur `station_id` une seule fois au chargement. Vérifié : 1 519 identifiants entiers de part et d'autre, intersection parfaite, zéro orphelin. |
| 2 | **Tableau biscornu** | `num_bikes_available_types` vaut `[{"mechanical":3},{"ebike":4}]` — un tableau d'objets à une clé, **pas** un objet. Un décodage en `map[string]int` renvoie zéro sans erreur. | Décodage en `[]map[string]int`, fusion **par clé** et non par position. Invariant vérifié en test live : mécaniques + électriques = total. |
| 3 | **« Hors service » indéfini** | Trois drapeaux, trois réponses : `is_installed=0` → 1 station ; `is_renting=0` → 16 ; au moins un à 0 → 16. Combinaisons réelles : 1 503 en (1,1,1), 15 en (1,0,0), 1 en (0,0,0). | Définition orientée usager choisie **et déclarée**, y compris dans le champ `out_of_service_rule` renvoyé au modèle avec le chiffre. |
| 4 | **Recherche par nom** | 532 stations sur 1 519 portent des accents. 3 noms sont en double. « gare de lyon » correspond à 3 stations. « Benjamin Godard » ne correspond à aucun nom exact. | Normalisation NFD + suppression des diacritiques. Retour d'une **liste de candidats**, jamais d'une station unique, avec un indicateur `ambiguous`. |
| 5 | **Liste non bornée** | Stations vides : 49 puis 115 en une heure. | `{total, sample, truncated}`, échantillon borné **côté serveur** à 10. |
| 6 | **Données sales** | 4 stations à `capacity = 0` (division par zéro). 15 stations où `bikes + docks > capacity` (physiquement impossible). | `OccupancyRate()` renvoie `(valeur, exploitable bool)` et refuse de produire un chiffre faux. Les bornes libres sont **lues** dans `num_docks_available`, jamais calculées par soustraction. |
| 7 | **Fraîcheur trompeuse** | La documentation des API annonce « rafraîchi toutes les minutes ». C'est vrai du **fichier**, pas des stations : 441 stations muettes depuis plus d'une heure, la plus ancienne à 48 499 h (~5,5 ans, valeur sentinelle). | Chaque réponse d'outil porte un bloc `freshness`. Une station dont la remontée dépasse une heure porte son âge ; les autres non, pour ne pas gaspiller de contexte. |

### La mesure qui justifie toute l'architecture

Test `TestLiveContextReduction`, sur les données réelles :

```
parc brut sérialisé : 446 157 octets
  network_summary       454 octets   (réduction  982x)
  rank_stations         915 octets   (réduction  487x)
  count_stations      1 591 octets   (réduction  280x)
  find_station          240 octets   (réduction 1858x)
```

Un test échoue automatiquement si une sortie d'outil dépasse deux ordres de
grandeur sous le parc brut. Le jour où quelqu'un ajoutera un champ verbeux, la
suite le dira.

---

## 3. Ce qui a été généré, ce que j'ai relu, ce que j'ai corrigé

La spécification annonce la question sans détour. Voici la réponse, tenue au fil de
l'eau.

### Ce que les tests ont attrapé que la relecture avait laissé passer

Le défaut n° 4 mérite d'être raconté : il n'a été trouvé ni en écrivant le code,
ni en le relisant, mais en écrivant des tests qui appellent les outils **par leur
vraie interface** (`tool.CallableTool`) plutôt que par la fonction Go interne.

C'est la différence entre tester le calcul et tester le contrat. Le calcul était
juste ; c'est le chemin réel qui était cassé, dans un cas que la production
n'exerce pas encore mais qu'un second chemin de chargement aurait exercé demain.

### Ce que j'ai décidé moi-même, sans délégation

- **Le découpage en paquets** et la frontière entre eux.
- **La conception des quatre outils** : leur nombre, leurs paramètres, et
  surtout la forme de leur retour. C'est le cœur du problème, je ne l'ai pas
  délégué.
- **Le choix `{total, sample}`** plutôt qu'une liste.
- **La définition de « hors service »** et le fait de la renvoyer au modèle.
- **La politique de cache**, en particulier le service de la donnée périmée en
  cas de panne de la source plutôt qu'une erreur.
- **Les cas de test** : chaque test encode un piège mesuré, pas une ligne de
  couverture.

### Ce qui a été largement généré, puis relu ligne à ligne

- Le code d'infrastructure : `Dockerfile`, `docker-compose.yml`, handlers HTTP,
  plomberie SSE.
- Les structures de décodage JSON.
- Le front.

### Corrections concrètes

*(Section tenue à jour pendant tout le développement — chaque entrée est un
exemple utilisable en soutenance.)*

| # | Ce qui a été produit | Ce qui n'allait pas | Correction |
|---|---|---|---|
| 1 | Décodage de `num_bikes_available_types` en `map[string]int` | Compile, ne lève aucune erreur, et renvoie **zéro vélo électrique** sur tout le parc. Un bug silencieux, le pire genre. | `[]map[string]int` + fusion par clé. Deux tests dédiés, dont un qui inverse l'ordre des éléments. |
| 2 | `OccupancyRate()` renvoyant un simple `float64` | Division par zéro sur les 4 stations à capacité nulle → `+Inf` propagé jusqu'au modèle. | Signature `(float64, bool)` : le calcul refuse de produire un chiffre indéfendable. |
| 3 | Import `github.com/trpc-group/trpc-agent-go` | Ne compile pas : le module est publié sous *vanity path* `trpc.group/...`. | Vérifié dans le `go.mod` du dépôt avant d'écrire. |
| 4 | `Search()` lisant directement le champ privé `searchKey` | **Trouvé par un test, et c'est le défaut le plus intéressant du lot.** `searchKey` n'est rempli que par `join()`. Une `Station` construite par un autre chemin avait donc une clé vide, et la recherche renvoyait « aucun résultat » — **sans erreur, sans journal**. Une structure à moitié initialisée dont la panne est silencieuse. | Ajout de `normalizedName()` : la valeur pré-calculée si elle existe, sinon normalisation à la volée. Le chemin de production reste rapide, et plus aucun chemin ne peut échouer en silence. |

---

## 4. Décisions écartées, et pourquoi

- **Une couche de persistance maison** — écartée après lecture de
  `session/postgres`. Le framework fait déjà exactement ce que la spécification demande.
- **La dépendance `singleflight`** — écartée. Vingt lignes avec un mutex et un
  channel suffisent pour une seule clé, et se défendent en revue. Une dépendance
  de plus pour ça n'aurait pas été justifiable.
- **Une distance de Levenshtein pour la recherche** — écartée. Plus fine sur le
  papier, elle fait remonter des noms qui ne partagent aucun mot avec la
  requête. Trois paliers explicites sont moins impressionnants et beaucoup plus
  faciles à défendre quand un résultat surprend.
- **Le géocodage et la météo** (extensions optionnelles) — écartés. Aucune des cinq
  questions n'en a besoin, et la spécification dit lui-même que trois choses qui
  tiennent debout valent mieux que dix à moitié branchées.

---

## 5. Ce qu'un second passage a trouvé

Le projet marchait à la fin de la section 4. Tout ce qui suit a été trouvé
**après**, en cherchant activement à le casser plutôt qu'à le finir.

### Changer de fournisseur révèle ce qu'on n'a pas écrit

Le développement s'est fait contre Groq. Passer à Gemini, puis à Cerebras, a
sorti quatre défauts d'un coup — tous invisibles tant qu'on ne teste qu'un seul
fournisseur.

| Symptôme | Cause réelle |
|---|---|
| 400 dès le second tour | Le framework rejoue `reasoning_content` dans l'historique, Groq refuse ce champ en entrée |
| 400 sur un classement | Le générateur de schéma marque **tout** obligatoire sauf `omitempty` : le modèle omettait `ascending` à juste titre |
| `unsupported protocol scheme ""` | Sans `OPENAI_BASE_URL`, aucune URL n'est posée — la configuration **par défaut** du projet ne démarrait pas |
| 402, 403, 404 rendus « une erreur est survenue » | Trois pannes parfaitement diagnosticables noyées dans un message générique |

La leçon qui vaut d'être dite en soutenance : **je codais contre la tolérance
d'un fournisseur, pas contre une spécification.** Le durcissement du périmètre a
ensuite été écrit contre Groq et vérifié contre Gemini sans toucher au code —
un garde-fou qui ne tient que sur le modèle qui a servi à l'écrire n'est pas un
garde-fou.

### Le premier log qu'il a fallu réparer

Avant de pouvoir diagnostiquer quoi que ce soit, il a fallu corriger le
journal lui-même. Il affichait `objet=""` : le code loggait `Response.Object`,
qui ne porte que le type d'événement, alors que le détail vit dans
`Response.Error`. Une erreur signalée, impossible à diagnostiquer.

C'est le premier correctif de la série, et sans lui aucun des suivants n'était
trouvable.

### Ce que la mesure a démenti

Trois fonctions que j'aurais optimisées d'instinct ne coûtaient rien :
`Search` à 350 µs, les sorties d'outils à 110–738 jetons, le prompt statique à
2 599 jetons. La preuve que le prompt n'est pas le levier tient en deux
mesures : 5 470 jetons → 1,5 s, 5 972 jetons → 26,1 s. Prompt quasi identique,
latence dix-sept fois supérieure.

Les deux qui décrochaient à un million de stations n'étaient pas dans ma liste :
`Rank` triait tout le parc pour rendre cinq stations (1 096 ms → 57 ms), et
`Search` appelait `strings.Fields` **dans** la boucle, une fois par station
(1 515 242 allocations → 43).

### La panne que seule la concurrence révèle

Tout avait été mesuré en série. À 200 clients simultanés, des centaines de 500 :
`FATAL: sorry, too many clients already`. Le service de sessions du framework
ouvre sa base sans borner le pool, et `database/sql` autorise alors un nombre
illimité de connexions.

Borner à 64 requêtes en vol a **augmenté** le débit de 30 % et divisé la latence
p95 par trois, en plus de supprimer les erreurs. Contre-intuitif, et c'est
précisément pour ça que ça mérite d'être mesuré plutôt que supposé.

### Mes erreurs, puisque ce journal les inclut

**J'ai chronométré la file d'attente en croyant mesurer des modèles.** Un banc
comparait trois modèles et donnait des écarts nets. Puis trois questions ont
rendu ~21 000 ms *exactement* — trop uniforme pour être de la latence. C'était
le palier gratuit qui limite les jetons par minute et fait **attendre** au lieu
de rejeter. Le classement des modèles est donc à prendre avec prudence, et
c'est écrit tel quel dans le README.

**J'ai écrit deux fois un audit tout vert sur des cas jamais exécutés.** D'abord
une identité unique pour tout le fichier : ma propre limite de débit renvoyait
429 aux cas suivants. Puis, en corrigeant, une identité par appel : la
conversation appartenait à l'une et le message partait sous une autre, donc 404
partout. Deux fois un rapport parfait sur du vide — pire que pas d'audit.

**J'ai failli annoncer deux défauts qui n'existaient pas.** Un tiroir latéral qui
semblait ne pas s'ouvrir : c'était le panneau de test masqué, où les transitions
CSS ne progressent pas. Et des 500 sous charge que je n'arrivais plus à
reproduire : c'est la charge *soutenue* qui épuise le pool, pas la pointe.

**J'ai réécrit deux fois du code de la bibliothèque standard** — `strconv.Itoa`
et `strings.Contains` — dans des fichiers que je venais d'écrire. Corrigé, mais
c'est le genre de réflexe qui passe une relecture rapide.

### Ce que j'ai refusé de faire

`internal/httpapi` reste à 26 % de couverture et **j'y laisse**. Ces
gestionnaires sont testés par la suite d'intégration, contre le vrai PostgreSQL
et le vrai routage. Ajouter 150 lignes de doublure de session pour afficher
60 % testerait *moins* pour plus cher — du théâtre de couverture.

De même, la lecture de fichiers, de PDF et d'images a été écartée malgré la
tentation : un agent de stations Vélib' qui lit des images n'a pas de sens
produit, et l'ajouter contredirait la règle de périmètre qui bloque justement
les demandes hors sujet.
