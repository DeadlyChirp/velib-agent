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
