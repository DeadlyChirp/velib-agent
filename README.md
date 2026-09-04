# Agent conversationnel — parc Vélib' Paris

Un agent qui répond en langage naturel sur les 1 519 stations Vélib' de Paris et
sa métropole. API Go avec `trpc-agent-go`, conversations persistées en
PostgreSQL, réponse en streaming, front statique. `docker compose up` et c'est
en ligne.

---

## Lancer

```bash
cp .env.example .env      # puis renseigner OPENAI_API_KEY
docker compose up --build
```

Le front est sur **http://localhost:3000**, l'API sur **http://localhost:8080**.

Le modèle se choisit par variable d'environnement. Tout fournisseur compatible
OpenAI convient — OpenAI, Groq, Mistral, DeepSeek, ou un Ollama local — en
changeant `MODEL_NAME` et `OPENAI_BASE_URL`. Les combinaisons testées sont dans
`.env.example`.

Pour essayer sans carte bancaire, une clé Groq est gratuite et immédiate sur
[console.groq.com/keys](https://console.groq.com/keys) :

```bash
MODEL_NAME=llama-3.3-70b-versatile
OPENAI_API_KEY=gsk_...
OPENAI_BASE_URL=https://api.groq.com/openai/v1
```

```bash
make test        # 18 tests unitaires, sans réseau ni base
make test-live   # tests contre la vraie API Vélib'
make reset       # arrête la pile ET efface les conversations
```

---

## Les décisions, et pourquoi

### 1. Aucun outil ne renvoie la liste des stations

C'est la seule décision qui compte vraiment, et tout le reste en découle.

Le parc pèse **748 618 octets** pour 1 519 stations, et la source ne filtre pas
côté serveur : c'est tout ou rien. Un outil qui renverrait le parc reproduirait
exactement le problème que la spécification demande de résoudre.

Les quatre outils agrègent donc **en Go** et ne rendent que de petits résultats
bornés. Mesuré sur les données réelles :

```
parc brut sérialisé : 446 157 octets
  network_summary       454 octets   (réduction  982x)
  rank_stations         915 octets   (réduction  487x)
  count_stations      1 591 octets   (réduction  280x)
  find_station          240 octets   (réduction 1858x)
```

Un test (`TestToolOutputsStaySmall`) échoue si une sortie dépasse cette borne :
l'invariant est protégé, il ne se dégradera pas discrètement.

### 2. « Toutes les stations vides » renvoie un compte, pas une liste

La cinquième question de référence demande une liste non bornée. J'ai mesuré deux
fois le même jour, à une heure d'intervalle :

| | 10 h | 11 h |
|---|---|---|
| Stations vides | 49 (3,2 %) | **115 (7,6 %)** |

Rien dans le code n'avait changé — seulement la météo et l'heure. Un dimanche
soir de pluie, ce serait plusieurs centaines.

`count_stations` renvoie donc `{total, sample, truncated}` : le **total est
exact**, l'échantillon est borné à dix côté serveur. L'agent répond « il y en a
115, en voici dix ». Ça répond mieux à l'intention réelle — savoir *combien* —
et la taille de sortie ne dépend plus de la météo.

### 3. « Hors service » : j'ai choisi une définition, et je la publie

La spécification demande le pourcentage de stations hors service sans définir le terme.
Trois drapeaux existent et donnent trois réponses :

| Définition | Stations | Part |
|---|---|---|
| `is_installed = 0` seul | 1 | 0,07 % |
| `is_renting = 0` | 16 | 1,05 % |
| au moins un drapeau à 0 | 16 | 1,05 % |

J'ai retenu la définition **orientée usager** : une station est hors service si
elle est démontée, ne prête plus ou ne reprend plus de vélo. Elle est renvoyée
au modèle avec le chiffre, dans le champ `out_of_service_rule`, pour qu'il
puisse la citer plutôt que d'énoncer un pourcentage sans contexte.

Un chiffre sans sa définition est indéfendable devant un client.

### 3 bis. Les classements excluent les stations hors service

C'est la correction la plus importante du projet, et elle vient d'une relecture
adverse plutôt que de mon premier jet.

Une station hors service annonce souvent **beaucoup** de bornes libres —
précisément parce qu'elle ne reprend plus de vélo. Sans filtre, le top 5 par
bornes libres remontait, mesuré le 04/09 :

| | Station | Bornes libres | État |
|---|---|---|---|
| 1 | Station Tour de France | 200 | **hors service**, donnée de 955 h |
| 2 | Championnats d'Europe de Natation | 200 | **hors service**, 449 h |
| 3 | Hippodrome de Paris Vincennes | 97 | **hors service**, 295 jours |

La réponse à la question 3 de référence était donc fausse, et fausse avec assurance :
trois endroits où l'on ne peut rendre aucun vélo, présentés comme les meilleurs.

`Rank` filtre désormais les stations hors service pour les métriques de service,
et **le dit au modèle** (`22 stations hors service exclues du classement`). Le
filtre ne s'applique pas à `capacity`, qui décrit la taille physique : la plus
grande station reste la plus grande même fermée.

Le classement signale aussi les **ex aequo**. Sur « les stations avec le moins de
vélos », des dizaines sont à zéro : sans ce signal, le modèle présenterait vingt
noms départagés à l'alphabet comme s'il s'agissait d'un palmarès.

### 4. La recherche renvoie des candidats, jamais « la » station

Mesuré sur le parc réel : **532 stations sur 1 519 portent des accents**, trois
noms sont en **double**, et « gare de lyon » correspond à **trois stations
distinctes**. « Benjamin Godard », la question de référence, ne correspond à aucun
nom exact — le nom réel est « Benjamin Godard - Victor Hugo ».

La recherche normalise donc la casse et les accents, et renvoie jusqu'à trois
candidats avec un indicateur `ambiguous`. Choisir arbitrairement, c'est répondre
faux avec assurance.

Elle renvoie aussi **`total_matches`**, le nombre réel de correspondances, distinct
du nombre montré. Mesuré : « place » correspond à **186 stations**, « gare » à 67,
« mairie » à 31. Sans ce compteur, le modèle croirait qu'il en existe trois et
annoncerait un résultat complet alors que la station visée peut être absente de
la liste.

### 5. Le cache sert la donnée périmée plutôt qu'une erreur

Sans cache, chaque appel d'outil retéléchargerait 464 Ko. Le référentiel est
quasi statique, l'état bouge à la minute : TTL de 60 secondes, et un
singleflight maison pour qu'une expiration ne déclenche qu'**un seul**
rafraîchissement même sous dix requêtes simultanées.

Le comportement en panne est délibéré : si la source est injoignable, on sert la
dernière donnée connue **en la marquant périmée**, avec son âge. Un agent qui
reçoit une erreur brute comble le vide en inventant ; un agent qui reçoit « voici
la donnée, elle a quarante minutes, la source est injoignable » peut le dire
honnêtement.

Le rafraîchissement se fait **en arrière-plan**, pas dans la requête. La source
met 300 à 430 ms par fichier et le cache en charge deux : quand il bloquait,
une requête par minute payait ~700 ms pour tout le monde. Passé le TTL, on sert
la donnée en mémoire immédiatement et on rafraîchit dans une goroutine. La
donnée a au plus une minute de retard — invisible sur un comptage de vélos,
contrairement à l'attente.

Garde-fou : au-delà de dix fois le TTL, on redevient bloquant. Si la source est
tombée depuis dix minutes, l'appelant doit l'apprendre, pas recevoir des chiffres
d'un quart d'heure comme s'ils étaient frais.

### 6. Les sessions PostgreSQL viennent du framework

J'avais commencé à écrire ma propre table de messages. En lisant le code de
`trpc-agent-go` avant de continuer, j'ai trouvé `session/postgres`, dont
l'interface expose exactement les quatre opérations demandées : `CreateSession`,
`ListSessions`, `GetSession`, `DeleteSession`.

J'ai supprimé ma couche. Moins de code à maintenir, et un comportement de reprise
de session fidèle à ce que le Runner attend plutôt qu'une réimplémentation
approximative.

### 7. Les données sales, traitées explicitement

Trois anomalies mesurées sur le parc réel, dont deux font planter du code naïf :

- **4 stations ont `capacity = 0`.** Aucun calcul ne divise par ce champ. J'avais
  d'abord écrit une méthode `OccupancyRate()` protégée contre la division par
  zéro — un audit du code a montré qu'elle n'était **appelée nulle part**. Du
  code mort avec des tests et une place dans cette documentation : je l'ai
  retirée, et la protection réelle est celle du point suivant.
- **15 stations ont `bikes + docks > capacity`**, physiquement impossible. Les
  bornes libres sont donc **lues** dans `num_docks_available`, jamais calculées
  par soustraction.
- **`num_bikes_available_types` est un tableau d'objets à une clé**
  (`[{"mechanical":3},{"ebike":4}]`), pas un objet. Un décodage en
  `map[string]int` compile, ne lève rien, et renvoie zéro vélo électrique sur
  tout le parc. Décodé en `[]map[string]int`, fusionné **par clé** et non par
  position.

### 8. La fraîcheur est exposée au modèle

La documentation des API annonce un rafraîchissement « toutes les minutes ». C'est vrai du
**fichier**, pas des stations : mesuré, **441 stations muettes depuis plus d'une
heure**, la plus ancienne à 48 499 heures — une valeur sentinelle.

Chaque réponse d'outil porte un bloc `freshness`, et une station dont la
remontée dépasse une heure porte son âge. Les autres non : ajouter un champ
« tout va bien » à chaque ligne gaspillerait du contexte pour rien.

### 9. SSE plutôt que WebSocket

Le flux est unidirectionnel. SSE est du HTTP simple, se reconnecte tout seul
côté navigateur et traverse les proxys sans négociation. Un WebSocket aurait
ajouté une machine à états bidirectionnelle pour un besoin qui ne l'est pas.

Deux détails qui cassent le streaming en silence, traités : `proxy_buffering
off` côté nginx, et une méthode `Flush()` sur l'enveloppe de journalisation —
sans elle, l'assertion `http.Flusher` échoue et la réponse arrive d'un bloc à la
fin, sans qu'aucune erreur n'apparaisse nulle part.

### 10. Un front d'un seul fichier

Pas de React, pas de `node_modules`, pas d'étape de build. L'écran compte une
liste et une zone de conversation ; l'image finale est un nginx qui sert un
fichier statique. Ce qui est fait sérieusement quand même : streaming token par
token, affichage des appels d'outils en cours, gestion des erreurs, navigation
au clavier, et `textContent` partout — jamais `innerHTML`, puisque le contenu
vient d'un modèle.

---

---

## Ce qu'une relecture adverse a changé

Le code a été relu par cinq angles séparés — sécurité, architecture Go,
fiabilité, conception d'agent, et une lecture « jury » —, puis chaque constat a
été contre-interrogé pour éliminer les faux positifs. Ce qui en est sorti :

| Constat | Correction |
|---|---|
| **`Rank` ignorait `OutOfService`** : le top 3 de la question 3 était intégralement composé de stations fermées | filtre + signalement au modèle, deux tests dont un sur données réelles |
| Les descriptions de schéma étaient **coupées à la première virgule** par le générateur de tags : le modèle ne voyait ni le plafond de 20, ni le sens de `ascending=false` | descriptions réécrites sans virgule, schéma vérifié en le sérialisant |
| `find_station` comptait les résultats **renvoyés**, pas les correspondances réelles | ajout de `total_matches` et `truncated` |
| `network_summary` exposait un paramètre fantôme `unused` | struct vide — le générateur l'accepte, contrairement à ce que je croyais |
| `OccupancyRate()` était du **code mort** loué dans ce README | retiré, la vraie protection documentée à sa place |
| `TurnRecord.DataStale` était déclaré mais **jamais renseigné** | renseigné depuis les retours d'outils |
| `deriveTitle` coupait à l'octet 60, cassant un caractère accentué | découpe en runes |
| `CORS_ORIGINS` valait `*` par défaut, sans justification | défaut restreint à l'origine du front |

Le journal complet est dans [`NOTES.md`](NOTES.md).

---

## Ce que j'ai écarté, et pourquoi c'était un choix

- **Le géocodage d'adresses et la météo** (extensions optionnelles). Aucune des cinq
  questions n'en a besoin. La spécification dit que trois choses qui tiennent debout
  valent mieux que dix à moitié branchées : j'ai préféré durcir ce qui existe.
- **Le filtrage par arrondissement.** Les données Vélib' ne portent pas
  d'arrondissement, et un cinquième des stations est hors Paris. Le faire
  proprement demandait un appel de géocodage par station.
- **Une authentification.** Non demandée. L'en-tête `X-User-ID` isole les
  conversations, et le modèle de données du framework est déjà
  multi-utilisateurs : brancher un vrai jeton ne demanderait que de remplir cet
  en-tête depuis une identité vérifiée. **Tel quel, un utilisateur qui change
  l'en-tête voit les conversations d'un autre** — acceptable pour un test, à
  fermer avant toute mise en ligne.
- **La dépendance `golang.org/x/sync/singleflight`.** Vingt lignes avec un mutex
  et un channel font le travail pour une seule clé, et se défendent en revue.
- **Une distance de Levenshtein** pour la recherche. Plus fine sur le papier,
  elle fait remonter des noms qui ne partagent aucun mot avec la requête. Trois
  paliers explicites sont moins impressionnants et beaucoup plus faciles à
  expliquer quand un résultat surprend.

---

## Performance : ce que la mesure a dit

Profilage d'abord. Les bancs sont dans `internal/velib/bench_test.go`, relançables
avec `go test ./internal/velib -bench . -benchmem`.

**Trois suspects évidents, tous innocents :**

| Suspect | Mesuré | Verdict |
|---|---|---|
| `Search` sur 1 519 stations | **350 µs** | quatre ordres de grandeur sous un tour d'agent |
| Sorties d'outils | 110 à 738 jetons | déjà compactes |
| Prompt statique | 2 599 jetons | instruction + quatre schémas |

Optimiser la recherche aurait été du travail visible pour un gain nul : un tour
d'agent prend 1,5 à 26 secondes, et 350 µs en représentent 0,001 %.

**Le prompt n'est pas le levier non plus**, et la mesure le montre directement :
une question à 5 470 jetons de prompt répond en 1,5 s, une autre à 5 972 jetons
met 26,1 s. Prompt quasi identique, latence dix-sept fois supérieure — c'est la
génération qui coûte, pas la lecture. Chaque règle de l'instruction bloque une
panne précise (hallucination, ambiguïté, troncature, donnée périmée) : la rogner
échangerait de la justesse contre des jetons qui ne coûtent rien.

**Le vrai goulot était le cache**, et il était invisible en développement : le
rafraîchissement bloquait la requête qui tombait sur l'expiration. Corrigé en
servant la donnée en mémoire pendant que la goroutine rafraîchit — détail en
section 5.

**Le second levier est le raisonnement du modèle.** Un tour ne fait qu'un seul
appel d'outil : la durée est presque entièrement de la génération, et sur un
modèle raisonneur la majorité des jetons produits sont invisibles dans la
réponse. Mesuré en isolation sur `gpt-oss-120b` :

| `reasoning_effort` | Latence | Jetons de complétion |
|---|---|---|
| défaut | 1 066 ms | 201 |
| **low** | **587 ms** | **98** |

Sur une question réelle, l'écart est plus net encore : 386 jetons au défaut
contre **20** en `low`, pour la même réponse juste. Le compromis est sans danger
ici parce que le modèle ne calcule rien — les agrégations sont faites en Go, de
façon déterministe. Il lui reste à choisir l'outil et rédiger.

D'où `REASONING_EFFORT=low` par défaut dans `.env.example`. Le paramètre n'est
**envoyé que s'il est renseigné** : il n'existe pas chez tous les fournisseurs,
et un Mistral rejetterait un champ inconnu.

### Une mesure ratée, et pourquoi je la raconte

Ma première comparaison de modèles donnait `gpt-oss-120b` à 13 s, `gpt-oss-20b` à
22 s, `qwen3.8` à 39 s. Puis un banc a rendu trois questions à ~21 000 ms
**exactement** — trop uniforme pour être de la latence.

C'était le palier gratuit de Groq : la limite est en jetons par minute, et
l'API ne rejette pas, elle **fait attendre**. Je chronométrais la file d'attente,
pas le modèle. Le classement des modèles est donc à prendre avec prudence — les
derniers testés partaient avec un seau déjà vidé par les premiers.

Ce qui reste solide : les mesures isolées, faites une par une avec le quota
reconstitué. Et le fait que le classement soit contaminé est lui-même une
information — sur ce palier, **c'est le débit qui contraint, pas la latence**.
Diviser les jetons par cinq multiplie d'autant le nombre de questions possibles
par minute.

La leçon tient en une ligne : les trois choses que j'aurais optimisées d'instinct
ne coûtaient rien, celle qui coûtait ne se voyait pas sans chronomètre, et mon
premier chronomètre mesurait autre chose que ce que je croyais.

---

## Avec plus de temps

1. **Historiser les snapshots** pour répondre aux questions de tendance
   (« cette station est-elle souvent vide le matin ? »). C'est la seule
   fonctionnalité qui changerait la nature du produit plutôt que de l'élargir.
2. **Tests de bout en bout** sur le flux SSE et le cycle de vie des
   conversations. Les agrégations sont couvertes, la couche HTTP ne l'est pas.
3. **Traces OpenTelemetry.** Le framework les expose, et Langfuse est un backend
   OTLP : le coût et la latence par outil deviendraient visibles sans code
   supplémentaire.
4. **Fermer l'usurpation d'identité** de `X-User-ID` derrière une vraie
   authentification.

---

## Architecture

```
web/          front statique servi par nginx, proxy /api vers l'API
api/
  cmd/api/            démarrage, arrêt propre, mode sonde
  internal/config/    lecture et validation de l'environnement
  internal/velib/     client HTTP, cache, jointure, AGRÉGATIONS (fonctions pures)
  internal/tools/     les 4 outils exposés au modèle + instruction système
  internal/agent/     câblage Runner, modèle, sessions PostgreSQL
  internal/httpapi/   routeur, CRUD conversations, streaming SSE
```

La frontière qui compte est celle de `internal/velib` : les agrégations sont des
fonctions pures qui prennent un `Snapshot` et rendent une petite valeur. Elles se
testent **sans réseau, sans base et sans LLM**, ce qui a permis de prouver que
les cinq questions de référence avaient la bonne réponse avant même de brancher un
modèle.

Le journal de bord — mesures, pièges, et ce qui a été généré puis corrigé — est
dans [`NOTES.md`](NOTES.md).
