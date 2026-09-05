# Fiabilité mesurée, par modèle

La spécification laisse le modèle libre, choisi par variable d'environnement. Ce
document dit ce que ce choix change **en pratique**, mesuré plutôt que supposé.

Reproduire : `python audit/fiabilite_modele.py --tours 10`

## Ce qui est mesuré

Les cinq questions de référence, posées N fois chacune contre la pile réelle. Le
script ne juge pas la justesse des chiffres — `audit/evaluation.py` s'en charge
en les comparant aux données Vélib' — mais la **forme** de la réponse, parce que
c'est là que les petits modèles cassent :

| Verdict | Ce qu'il signifie |
|---|---|
| `FUITE` | la syntaxe d'appel d'outil arrive en clair dans la réponse |
| `SANS OUTIL` | aucun outil appelé : les chiffres ne peuvent venir que du modèle |
| `NARRATION` | le modèle annonce ce qu'il va faire au lieu de le faire |
| `SANS CHIFFRE` | réponse longue mais sans le moindre nombre |
| `TRONQUÉE` | moins de 25 caractères |

**Ces taux sont un plafond, pas une garantie.** Le script ne voit pas une
réponse bien formée dont les chiffres sont faux. Observé en vrai : un modèle a
appelé l'outil, reçu la bonne donnée, puis cité une station absente du résultat
en précisant lui-même qu'elle n'y figurait pas. La forme était irréprochable.

## Une mesure jetée, et pourquoi elle est ici

La première comparaison donnait `qwen2.5:7b` à **80 %**. La seconde, avec un
détecteur pourtant devenu plus strict, donnait **98 %** pour le même modèle. Un
détecteur plus strict ne peut pas remonter un score : il y avait donc un facteur
externe, et c'en était un — le premier tour s'est exécuté pendant que l'autre
modèle se téléchargeait, en concurrence sur le disque et le GPU.

Les deux chiffres sont inutilisables et ne figurent pas dans le tableau
ci-dessous. C'est la deuxième fois dans ce projet qu'un banc mesure autre chose
que ce qu'il annonce — la première est racontée dans le README, sur le palier
gratuit de Groq. La leçon se répète : **un banc lancé pendant qu'autre chose
tourne ne mesure pas ce qu'on croit.**

Tout ce qui suit vient de quatre passes lancées à la suite, machine au repos,
même détecteur, `temperature` par défaut.

## Résultats

Matériel : RTX 4070 Laptop (8 Go VRAM), i7-14700HX, 32 Go RAM. Ollama 0.33.3.
Mesuré le 5 septembre 2026, 10 tours par question et par passe.

| Modèle | Passe 1 | Passe 2 | **Total** |
|---|---|---|---|
| **`qwen2.5:7b`** | 49/50 | 46/50 | **95/100 — 95 %** |
| `llama3.1:8b` | 43/50 | 46/50 | 89/100 — 89 % |

Par question, les deux passes cumulées :

| Question | Outil attendu | `qwen2.5:7b` | `llama3.1:8b` |
|---|---|---|---|
| Q1 vélos électriques | `network_summary` | 18/20 | 20/20 |
| Q2 pourcentage hors service | `network_summary` | 19/20 | 19/20 |
| **Q3 top 5 bornes libres** | `rank_stations` | **18/20** | **11/20** |
| Q4 station nommée | `find_station` | 20/20 | 20/20 |
| Q5 stations vides | `count_stations` | 20/20 | 19/20 |

## Ce que ça dit

**Q3 concentre presque tous les échecs, sur les deux modèles.** Deux hypothèses
ont été testées et **réfutées** avant de toucher au code — elles sont ici parce
qu'une piste écartée sur mesure vaut mieux qu'une piste jamais explorée.

*Hypothèse 1 : le paramètre booléen.* Q3 est la seule question dont l'outil
prend un booléen — `ascending`, à laisser à `false` pour « le plus de ». Dans
les fuites capturées, le modèle écrivait `"ascending": "false"`, une chaîne que
le désérialiseur Go refuserait. Testé en isolation, même question et même
modèle, deux schémas ne différant que par la forme du sens de tri :

| | booléen | énumération de chaînes |
|---|---|---|
| « le plus de bornes libres » | 15/15 | 15/15 |
| « le moins de vélos » | 15/15 | 15/15 |

60 appels, aucun échec des deux côtés. Le schéma d'outil n'est pas en cause et
n'a donc **pas** été modifié.

*Hypothèse 2 : le contexte tronqué.* Ollama plafonne historiquement à 4096
jetons, et le prompt statique de ce projet en pèse déjà 2 599. Une troncature
emporterait l'instruction système et expliquerait le comportement erratique.
`ollama ps` annonce **32768** : réfuté aussi.

*Ce qui était vraiment en cause : la température.* Elle était sous mes yeux
depuis le début — l'expérience isolée ci-dessus forçait `temperature: 0`, le
pipeline complet laissait le défaut du fournisseur, soit 0,7 pour ce modèle.
Toute la différence tenait là.

| Configuration | Fiabilité |
|---|---|
| défaut du fournisseur (0,7) | 95 % — 95/100 |
| **`MODEL_TEMPERATURE=0.1`** | **100 % — 150/150** |

Trois passes de cinquante, machine au repos, par le chemin de configuration
réel. C'est devenu le défaut du projet, et le raisonnement se défend sans les
chiffres : les outils calculent, le modèle ne fait que choisir un outil et
rédiger une phrase. La créativité n'a rien à y gagner et coûtait de la
constance de format.

**`llama3.1:8b` était mon pari initial, et il perd.** Je l'avais tiré en
pensant qu'un modèle explicitement entraîné à l'appel d'outil ferait mieux. Sur
Q3 il tombe à 55 %, avec huit fuites sur vingt tours.

**Les chiffres du tableau ci-dessus datent d'AVANT le réglage de température.**
Ils sont conservés parce qu'ils sont ce qui a permis de trouver le problème :
sans la comparaison entre deux modèles, la fragilité de Q3 serait passée pour
du bruit. Avec `MODEL_TEMPERATURE=0.1`, `qwen2.5:7b` tient 150/150.

`llama3.1:8b` n'a pas été re-mesuré après le réglage : il avait déjà perdu, il
a été retiré de la machine, et refaire cent tours pour départager un modèle
qu'on n'utilisera pas n'apprend rien.

## Ce qui n'a délibérément pas été fait

Le service pourrait détecter une fuite de syntaxe d'outil et la masquer. Ce
n'est pas fait, et c'est un choix : les marqueurs diffèrent d'un modèle à
l'autre — `<tool_call>` chez qwen, du JSON nu chez llama — et un filtre par
motifs se tromperait tôt ou tard sur une réponse légitime. Documenter la limite
et recommander un modèle vaut mieux qu'un filtre fragile qui donne l'illusion
d'un problème réglé.

## Recommandation

| Usage | Modèle |
|---|---|
| Démonstration, entretien, CI | un modèle hébergé — les six cas de `audit/evaluation.py` passent 6/6 sur `gemini-flash-lite-latest` |
| Local, hors ligne, sans quota | **`qwen2.5:7b`** avec `MODEL_TEMPERATURE=0.1` — 150/150 |

`.env.example` documente les deux chemins.
