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

`internal/httpapi` reste à 23 % de couverture et **j'y laisse**. Ces
gestionnaires sont testés par la suite d'intégration, contre le vrai PostgreSQL
et le vrai routage. Ajouter 150 lignes de doublure de session pour afficher
60 % testerait *moins* pour plus cher — du théâtre de couverture.

De même, la lecture de fichiers, de PDF et d'images a été écartée malgré la
tentation : un agent de stations Vélib' qui lit des images n'a pas de sens
produit, et l'ajouter contredirait la règle de périmètre qui bloque justement
les demandes hors sujet.

---

## 6. La passe de durcissement

Le projet était complet et vert à la fin de la section 5. Tout ce qui suit a été
trouvé en cherchant les défauts **là où aucun test ne regardait** : le chemin du
relecteur, la concurrence, l'arrêt, la CI, et la machine elle-même.

### Le parcours que personne n'avait fait

J'avais tout vérifié sauf ce qu'un relecteur fait en premier : **cloner et
lancer**. Fait sur un clone frais, ports décalés. Trois défauts, tous dans le
chemin qu'un développeur ne prend jamais parce qu'il a déjà tout installé.

| Défaut | Pourquoi invisible |
|---|---|
| `.env.example` livrait `REASONING_EFFORT=low` avec `gpt-4o-mini` | Ce champ n'existe que chez les modèles raisonneurs : la config **par défaut** échouait au premier message |
| `make test` échoue sans compilateur C | `-race` exige cgo, et l'erreur parle de cgo sans mentionner le compilateur |
| `make` absent sous Windows | Le README ne donnait que les cibles make |

### La panne à 200 clients

Tout avait été mesuré en série. En concurrence, `FATAL: sorry, too many clients
already` : le service de sessions du framework ouvre sa base avec `sql.Open`
sans jamais appeler `SetMaxOpenConns`, et `database/sql` autorise alors un
nombre illimité de connexions.

Le framework n'expose ni le `*sql.DB` ni d'option de pool. On ne peut pas
corriger la cause, seulement empêcher d'y arriver : 64 requêtes en vol, attente
brève, puis 503 avec `Retry-After`.

| Clients | Avant | Après |
|---|---|---|
| 200 | 685 req/s · **343 erreurs** | **920 req/s · 0 erreur** |
| 400 | 711 req/s · **716 erreurs** | **924 req/s · 0 erreur** |
| 1 000 | — | **949 req/s · 0 erreur** |

**Borner la concurrence a augmenté le débit de 30 %.** Contre-intuitif, et
c'est précisément pour ça qu'il fallait le mesurer.

### Une promesse cassée par un fichier de configuration

`main.go` accorde 25 secondes aux conversations en cours pour se terminer, avec
un commentaire qui explique pourquoi. Docker envoie SIGKILL au bout de **10**.

Le défaut ne se voyait que sur les tours de plus de dix secondes — mesurés
jusqu'à 26 s. Invisible en développement, visible le jour d'une mise en
production en pleine journée. Un test relit désormais les deux fichiers.

### La CI échouait sur chaque commit, et je ne l'avais jamais regardée

C'est l'erreur de méthode la plus embarrassante du projet. J'ajoutais des étapes
sans vérifier qu'elles passaient. **Tous les tests étaient verts** ; seule
l'étape de ménage échouait :

```
required variable OPENAI_API_KEY is missing a value
```

`docker-compose.yml` utilise l'opérateur `:?`, qui fait échouer *toute* commande
compose — y compris `logs` et `down`, qui ne démarrent rien. Le piège était
documenté en toutes lettres dix lignes plus haut, pour l'étape de construction,
et je l'ai quand même réintroduit.

*Un rouge de fin de job qui ne dit rien sur le code est exactement le genre de
rouge qu'on apprend à ignorer — et c'est ainsi qu'on rate le vrai.*

### La duplication que j'avais moi-même créée

En portant la typographie française sur le tableau de bord, je l'avais
**recopiée** au lieu de la partager. Les deux copies avaient déjà divergé :

| entrée | `index.html` | `tracker.html` |
|---|---|---|
| `« Châtelet »` | `«·Châtelet·»` | `« Châtelet »` |

Deux pages qui affichent le même chiffre doivent l'écrire pareil : c'était tout
l'intérêt du travail, et la duplication l'annulait en silence. Extrait dans un
fichier unique chargé par un `<script src>` — et l'extraction a failli casser le
front sans rien casser à la construction, parce que le Dockerfile copie les
fichiers un par un et que j'avais oublié le nouveau.

### La machine, pas le code

Deux problèmes qui n'étaient pas des défauts du projet mais qui le bloquaient.

**OneDrive cassait `docker build`.** Tag de point d'analyse `0x9000a01a`, le
marqueur « Fichiers à la demande » : 51 fichiers sur 61 le portaient, et
BuildKit refuse de lire un Dockerfile qui en est un. L'attribut survit au
déplacement — il a fallu recloner. Vérifié : le même fichier hors OneDrive se
construit sans problème.

**Les fins de ligne rendaient `make check` inutilisable.** `core.autocrlf=true`
convertit en CRLF à l'extraction, et `gofmt` signalait les 38 fichiers Go. La CI
ne le voyait pas : elle tourne sur Linux. *Un défaut qui ne se manifeste que sur
la machine du développeur est le pire des deux mondes.* Réglé par un
`.gitattributes` qui met la règle dans le dépôt plutôt que dans la config de
chacun.

### Ce qui est vérifié en continu, désormais

| Suite | Cas |
|---|---|
| Tests Go | 108 fonctions, 163 cas |
| Rendu front | 32 vérifications, dont six charges XSS |
| Injections HTTP | 30 |
| Matrice d'injections | 44 |
| Comportements | 21 |
| Cohérence de la documentation | 19 |

Elles tournent toutes en CI, persistance comprise. Les suites qui appellent le
modèle — dix injections de prompt, six cas d'évaluation — restent manuelles : elles coûtent des jetons, et
le palier gratuit plafonne à vingt requêtes par jour et par modèle.

### Mes erreurs de cette passe

**J'ai réintroduit un piège documenté dix lignes plus haut** (la clé manquante
en CI). La documentation ne protège pas de l'inattention.

**J'ai refait deux fois la même erreur de granularité** dans les scripts
d'audit : une identité par fichier fait tout finir en 429, une identité par
appel fait tout finir en 404. Les deux produisent un rapport vert sur du vide.

**Mon test d'arrêt mesurait autre chose que ce qu'il annonçait** : son
expression capturait le délai du préchauffage du cache et affichait un accord
parfait entre deux valeurs sans rapport.

**Mon test de charge attribuait sa propre limite au service.** À 2 000 clients
il rapportait des centaines d'échecs — tous côté client, sans aucun refus
journalisé. Vérifié séparément : 2 000 requêtes vraiment simultanées passent
sans une exception. C'est le harnais qui épuisait les ports éphémères.

**Mes motifs de détection ne connaissaient qu'une apostrophe.** Le modèle écrit
« n'existe » avec la typographique : le cas passait en DOUTE alors que la
réponse était juste. *Un audit qui sous-estime le service trompe autant qu'un
audit qui le surestime.*

**Et le vérificateur de cohérence avait lui-même un bogue en naissant.**

---

## Relecture de la spécification, 5 septembre

Relire la spécification après coup plutôt qu'avant a payé : il restait un défaut sur une
**exigence littérale**.

**« Un `.env.example` listant toutes les variables attendues » — trois
manquaient.** `API_ADDR`, `VELIB_INFORMATION_URL` et `VELIB_STATUS_URL` étaient
lues par `config.go` sans apparaître nulle part. Trois variables qu'on ne peut
pas découvrir sans lire le code, dans le fichier dont c'est précisément le rôle
d'éviter cette lecture. Le fichier avait aussi accumulé un doublon et
recommandait encore un modèle Groq retiré du catalogue.

Corriger ne suffisait pas : l'oubli reviendrait à la prochaine variable ajoutée.
`TestEnvExempleListeToutesLesVariables` lit `config.go`, lit `.env.example`, et
compare **dans les deux sens** — une variable lue mais non documentée échoue, une
variable documentée que plus personne ne lit échoue aussi, parce qu'elle envoie
le lecteur configurer quelque chose sans effet. Vérifié en le cassant exprès
avant de le croire.

**Un critère de qualité n'avait aucune section pour le défendre.** La spécification
annonce regarder « le comportement de l'agent quand il ne sait pas, ou quand un
appel échoue ». Le code le traitait bien — `ToolError` avec sa consigne,
`ambiguous`, `total_matches` à zéro — et un test le tenait. Mais le README, la
partie qu'ils disent regarder en premier, n'en parlait nulle part. Le travail
était fait, la défense manquait : section 5 bis.

**La couverture globale annoncée avait dérivé sans que rien ne le voie.** Le
vérificateur contrôlait les six couvertures par paquet et pas le total : 59 %
annoncé, 61 % réel. Il vérifie maintenant les deux, et le README publie le
**périmètre** avec le chiffre — 61 % sur `internal/`, 55 % en comptant `cmd/api`
dont le `main` n'a aucun test. Un pourcentage sans son périmètre se fait dire ce
qu'on veut.

**Et le vérificateur a re-planté sur lui-même.** Ajouter le comptage des
sous-tests l'obligeait à lire une sortie verbeuse : Python la décodait avec la
locale Windows et mourait sur le premier « d'un message de test. Deuxième bogue
de naissance pour ce script — il a en échange attrapé mes deux chiffres faux dès
la première exécution, dont un que j'introduisais moi-même en ajoutant un test.

---

## Audit adverse à 91 agents, 5 septembre

Neuf lentilles indépendantes sur le dépôt — conformité littérale à la spécification,
périmètre fonctionnel, démarrage à froid, conception des outils, README
défendable, qualité du code, secrets, historique git, front. Chaque constat
soumis à trois sceptiques ayant chacun une consigne différente : vérifier la
preuve, juger l'impact réel sur l'évaluation, et refuser la sur-ingénierie.
Un constat ne survivait qu'avec deux voix sur trois.

**Le plus grave était un signal mort que je croyais vivant depuis le début.**
La note d'ex aequo de `rank_stations` comptait les égalités dans la tranche
suivant les k retenus — mais dans la liste que `topK` venait de réduire à k
éléments. Toujours vide, garde toujours fausse, note jamais émise. Trois
endroits la promettaient au modèle, dont le README.

Aucun test ne l'avait vu parce que tous comparaient la **liste** rendue, jamais
la **note**. C'est la leçon la plus transférable du lot : un test qui vérifie le
résultat principal ne protège pas les signaux qui l'accompagnent, et ce sont
justement eux qui empêchent le modèle de sur-interpréter.

**Le cache mentait sur son chemin le plus fréquent.** « Servir tout de suite et
rafraîchir derrière » ne consultait pas le résultat du rafraîchissement
précédent : pendant une panne de la source, il rendait des chiffres périmés avec
`stale=false` et comptait des succès de cache. Le compteur qui existe pour rendre
l'incident visible restait à zéro pendant l'incident. Corrigé, avec les deux
tests qui tiennent les deux moitiés — marquer pendant la panne, ne pas marquer
tant que la source répond.

**Un commentaire affirmait le contraire du code.** « Le Runner finit son tour et
persiste ce qu'il a produit » alors qu'il reçoit le contexte de la requête, donc
la déconnexion l'annule. Le framework expose bien
`WithPersistInterruptedAssistant`, dont la valeur par défaut est `false` « to
preserve cancellation semantics ». Vu et écarté : une réponse tronquée qui entre
dans l'historique est relue au tour suivant comme si le modèle l'avait finie.

**Le README sous-vendait le travail le plus vérifiable.** « Avec plus de temps »
réclamait des tests de bout en bout sur le flux SSE et le cycle de vie des
conversations — `TestCycleDeVieConversation` et `TestFluxSSE` existent et
tournent à chaque poussée. La vraie lacune, elle, n'était nommée nulle part :
aucun test ne rejoue « envoyer un message, couper la pile, la relancer, relire
l'historique », qui est pourtant une exigence explicite de la spécification.

**Et j'ai commis moi-même deux fautes pendant cette session.** J'ai poussé sur
`main` alors que le dépôt est sur `master`, créant une branche en double. Puis
`git add -A`, juste après un `py_compile`, a fait entrer 81 Ko de bytecode
Python dans un commit dont le message n'en disait pas un mot — dans un dépôt
dont la spécification exige littéralement « un historique lisible ». `__pycache__/` et
`*.pyc` sont maintenant ignorés.

Le vérificateur de cohérence est passé de 12 à 21 contrôles : il vérifie
désormais le nombre de cas, la couverture globale avec son périmètre, le compte
du rendu front, et les mêmes chiffres **des deux côtés**, README et NOTES —
c'est là que trois valeurs fausses s'étaient logées.

**Et le correctif ne servait à rien sans un second.** Une fois la note d'ex
aequo enfin émise, le modèle la lisait et l'avalait : la description de l'outil
disait « lire le champ note », une instruction jamais exercée puisque la note
n'arrivait jamais. Remplacé par « le champ note doit être RESTITUÉ à
l'utilisateur ». Mesuré sur la même question, contre la pile réelle :

    avant   trois noms sur 82 ex aequo, présentés comme un palmarès
    après   « 79 autres stations affichent également 0 vélo disponible.
              22 stations hors service sont exclues de ce classement. »

Un signal que le modèle reçoit et n'utilise pas ne vaut pas mieux qu'un signal
absent. Calculer juste ne suffit pas, il faut dire au modèle quoi en faire — et
ça ne se voit qu'en regardant la réponse finale, pas la sortie de l'outil.

**Le code mort exporté, que staticcheck ne voit pas.** Trois symboles sans
appelant : `agent.ToolNames`, dont le commentaire promettait un usage par la
route de santé qui ne l'appelle jamais ; le champ `log` de `agent.Service`,
affecté et jamais lu ; et `velib.Search`, un enrobage de `searchTop` que seuls
les bancs empruntaient — ils mesuraient donc un chemin que la production ne
prend pas. Supprimés, les bancs pointés sur le vrai chemin. La couverture de
`velib` est montée de 88 à 90 % sans qu'un test soit ajouté : c'est ce que
signifie retirer du code que personne n'exécute.

En déplaçant `Search`, son bloc de documentation s'est retrouvé collé à celui de
`searchTop` — exactement le défaut godoc corrigé une heure plus tôt dans
`chat.go`. Fusionné plutôt que supprimé : le raisonnement qu'il porte, pourquoi
la recherche rend des candidats et jamais « la » station, vaut d'être gardé.

**Deux promesses qui ne tenaient pas hors du chemin nominal.** `.env.example`
annonce `VELIB_INFORMATION_URL` et `VELIB_STATUS_URL` surchargeables — absentes
du bloc `environment` du compose, elles ne l'étaient que hors Docker,
c'est-à-dire nulle part dans le parcours proposé. Et le README nommait
`TestToolOutputsStaySmall` comme garde-fou des tailles publiées, alors qu'il
tourne sur huit stations : le vrai garde-fou est
`TestTailleDeSortieIndependanteDuNombreDeStations`, qui rejoue les quatre outils
à 1 519 000 stations.

**La limite de débit, ce qu'elle ne fait pas.** Derrière le nginx du compose,
tous les navigateurs partagent l'IP du conteneur : sans `X-User-ID`, la limite
est de fait globale. Et un `X-User-ID` qui tourne la défait entièrement — mes
propres scripts d'audit le font par conception. Écrit dans le README plutôt que
laissé à découvrir. Le code ne bouge pas : forcer la clé IP casserait le produit
dans sa propre topologie, et tant que `X-User-ID` n'est pas authentifié, aucune
limite par identité n'est solide.

---

## La dernière exigence non testée, 5 septembre

La spécification écrit : « l'historique est persisté en base : on arrête la stack, on la
relance, tout est encore là ». C'était la seule de ses exigences qu'aucun test
ne rejouait, et je l'avais écrit noir sur blanc dans le README comme une lacune
assumée. Une vérification manuelle prouve que ça marchait ce jour-là, rien de
plus.

`audit/persistance.py` la rejoue : envoyer un message, `docker compose down`
**sans** `-v`, relancer, relire. Le détail qui compte est le `down` : un
`restart` de l'API ne prouverait rien, PostgreSQL n'ayant pas bougé. Le script
vérifie d'ailleurs que l'API ne répond PLUS entre les deux — sans ça, un arrêt
qui échouerait en silence ferait passer le test pour la pire des raisons, en
relisant la donnée d'un processus jamais interrompu.

**Deux modes, et il dit lequel il exécute.** La CI démarre la pile avec une clé
factice : aucun appel au modèle n'y aboutit. Plutôt que d'exiger un vrai modèle
et de rougir pour une raison sans rapport avec la persistance, le script bascule
en mode dégradé — il vérifie la survie de la conversation, pas celle du contenu
des messages — et l'**annonce** dans sa sortie. Un vert qui laisse croire qu'on
a tout vérifié vaut moins qu'un vert qui dit ce qu'il n'a pas regardé.

Vérifié dans les deux modes : 14 contrôles avec le modèle, 9 sans.

**Et le test a trouvé un défaut en s'écrivant.** Il affichait côte à côte
`message_count: 4` et un tableau `messages` de 2 entrées. `message_count` valait
`len(sess.Events)` — les événements du framework, appels d'outils compris —
pendant que `messages` n'expose que les tours affichables. Pire, la vue
« liste » n'a pas les événements du tout, puisqu'elle charge les sessions en
métadonnées seules par choix de performance : elle renvoyait donc
`"message_count": 0` pour toutes les conversations. Pas « aucun message », mais
« je n'en sais rien », écrit comme un fait.

Le champ compte maintenant le tableau qu'il accompagne, et a disparu de la vue
qui ne peut pas le calculer. Un zéro faux coûte plus cher qu'un champ absent :
le premier se croit, le second se remarque.

Aucun test ne les avait jamais comparés — il a fallu les imprimer l'un à côté
de l'autre pour que ça saute aux yeux.

---

## Ollama en local, et le réglage qui vaut plus que le modèle

Le projet tourne désormais sans clé et sans quota : `qwen2.5:7b` via Ollama,
`host.docker.internal` depuis le conteneur, ~3 s par réponse. Vérifié plutôt que
supposé : Docker Desktop relaie vers la boucle locale de l'hôte, donc Ollama
n'a besoin d'aucun réglage d'écoute, contrairement à ce que j'avais annoncé.

**Deux modèles mesurés, 100 tours chacun.** `qwen2.5:7b` 95 %, `llama3.1:8b`
89 %. Mon pari initial portait sur llama, entraîné explicitement à l'appel
d'outil : il perd, et perd sur une seule question — le classement, à 55 %.
Journal complet, protocole et matériel : `audit/mesures-modeles.md`.

**J'ai d'abord publié 80 % puis 98 % pour le même modèle**, avec un détecteur
pourtant devenu plus strict entre les deux. Un détecteur plus strict ne remonte
pas un score : il y avait un facteur externe, et c'en était un — la première
mesure tournait pendant le téléchargement de l'autre modèle. Deuxième fois dans
ce projet qu'un banc chronomètre autre chose que ce qu'il annonce. Les deux
chiffres ont été jetés et tout a été refait machine au repos.

**La vraie trouvaille n'est pas le modèle, c'est la température.** Le projet
n'en fixait aucune : chaque fournisseur appliquait la sienne. À 0.1, le même
modèle passe de 95 % à **150/150**. Les échecs supprimés étaient tous de la
syntaxe d'appel d'outil arrivant en clair dans la réponse.

Deux hypothèses ont été testées et réfutées AVANT de toucher au code : le
paramètre booléen de `rank_stations` — 60/60 des deux côtés en isolation, schéma
non modifié — et un contexte tronqué à 4096 jetons — `ollama ps` en annonce
32768. La variable était sous mes yeux : mon expérience isolée forçait
`temperature: 0`, le pipeline non.

**Deux pièges rencontrés en l'implémentant.**

`env()` traite le vide comme une absence et rend le défaut. `MODEL_TEMPERATURE=`
aurait donc rendu `0.1`, et les modèles raisonneurs d'OpenAI — qui refusent
toute température — n'auraient eu aucun moyen de la désactiver. Rien n'aurait
échoué au démarrage : le service part, le fournisseur rejette la première
question. D'où `envPosee`, et un `${VAR-0.1}` sans deux-points côté compose.

Et **mon propre test d'exhaustivité du `.env.example` avait un trou** : il
énumérait les helpers connus, `env` et `envDuration`. Le troisième lui a échappé
en silence et il est resté VERT sur un fichier incomplet — le défaut exact qu'il
existe pour empêcher, à un niveau d'indirection près. Il reconnaît maintenant
toute fonction dont le nom commence par `env`, et je l'ai vérifié en rouge
d'abord.

**Un test mal cadré, aussi.** `persistance.py` vérifiait que la réponse citait
« 1 519 » — donc la JUSTESSE, alors que son objet est la persistance. Il est
passé au rouge le jour où un modèle local a répondu par un fragment de schéma
JSON : un échec réel, mais du modèle, pas de la persistance. Un test qui rougit
pour une raison étrangère à son objet est un test qu'on apprend à ignorer.
L'assertion a été retirée, la justesse relevant de `evaluation.py`.
