# -*- coding: utf-8 -*-
"""À quel point l'agent est-il FIABLE, pour un modèle donné ?

    python audit/fiabilite_modele.py [--tours 5]

`evaluation.py` pose chaque question une fois et dit si la réponse est juste.
C'est suffisant tant que le modèle est déterministe dans son comportement — et
il ne l'est pas. Un modèle local de 7 milliards de paramètres répond
parfaitement à une question, puis échoue à la même question au tour suivant.
Une exécution unique note alors le hasard, pas le produit.

Ce script pose les cinq questions de référence N fois et compte. Il ne juge pas la
justesse des chiffres — `evaluation.py` s'en charge — mais la FORME de la
réponse, parce que c'est là que les petits modèles cassent :

  - l'outil a-t-il été réellement appelé ? (sinon les chiffres sont inventés)
  - la syntaxe d'appel d'outil a-t-elle FUITÉ en texte brut ?
  - la réponse est-elle vide, tronquée, ou sans le moindre chiffre ?
  - le modèle ANNONCE-t-il ce qu'il va faire au lieu de le faire ?

⚠️ Ce que ce script ne voit PAS : une réponse bien formée dont les chiffres
sont faux. Observé en vrai — un modèle a appelé l'outil, reçu la bonne donnée,
puis inventé une station absente du résultat en précisant lui-même qu'elle n'y
était pas. La forme était irréprochable. C'est `evaluation.py` qui compare aux
données réelles, et les deux scripts sont complémentaires : celui-ci mesure la
constance, l'autre la justesse. Les taux ci-dessous sont donc un PLAFOND, pas
une garantie.

⚠️ Ce n'est pas une barrière de CI. Un taux de réussite dépend du modèle
configuré, et faire échouer une CI parce qu'un modèle local est faible
n'apprend rien sur le code. C'est un instrument de mesure : on le lance quand on
change de fournisseur, et on écrit le résultat dans le README.
"""
import argparse
import json
import sys
import urllib.error
import urllib.request

# La console Windows encode en cp1252 par defaut. Ces scripts impriment du
# francais, et le premier caractere hors cp1252 — une fleche, un signe
# mathematique — tue le run EN PLEINE EXECUTION, apres avoir affiche des
# resultats verts. La CI tourne sous Linux en UTF-8 : elle ne le verra jamais.
# Une ligne par script, posee partout plutot qu'au coup par coup.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

API = "http://localhost:8080/api"

# Les cinq questions de référence, dans son ordre, plus l'outil qu'elles DOIVENT
# declencher. Une question qui n'appelle aucun outil ne peut repondre qu'avec
# la memoire du modele, c'est-a-dire en inventant.
QUESTIONS = [
    ("Q1 vélos électriques",
     "Combien de vélos électriques sont disponibles sur l'ensemble du parc ?",
     "network_summary"),
    ("Q2 pourcentage HS",
     "Quel pourcentage des stations est hors service ?",
     "network_summary"),
    ("Q3 top 5 bornes",
     "Quelles sont les cinq stations qui ont le plus de bornes libres ?",
     "rank_stations"),
    ("Q4 station nommée",
     "Combien de vélos y a-t-il à la station Benjamin Godard ?",
     "find_station"),
    ("Q5 stations vides",
     "Liste-moi toutes les stations vides.",
     "count_stations"),
]

# Marqueurs d'un appel d'outil qui a fui en texte. qwen2.5 emet du
# <tool_call>…</tool_call>, d'autres modeles emettent l'objet JSON nu. Aucun de
# ces fragments n'a de raison legitime d'apparaitre dans une reponse sur des
# stations de velos.
FUITES = ["</tool_call>", "<tool_call>", '{"name":', '"arguments"',
          "functions.", "<|python_tag|>",
          # Observe en vrai : au lieu d'appeler l'outil, le modele a recopie le
          # SCHEMA JSON de la sortie attendue. Ni <tool_call> ni "arguments"
          # n'apparaissaient — il a fallu ce troisieme marqueur.
          '"properties"', '"type": "object"']


def flux(question, identite):
    """Pose la question et rend (outils_appeles, texte_final)."""
    r = urllib.request.Request(
        API + "/conversations", data=b"{}", method="POST",
        headers={"Content-Type": "application/json", "X-User-ID": identite})
    conv = json.loads(urllib.request.urlopen(r, timeout=30).read())["id"]

    r = urllib.request.Request(
        API + "/conversations/%s/messages" % conv,
        data=json.dumps({"message": question}).encode(), method="POST",
        headers={"Content-Type": "application/json", "X-User-ID": identite})

    outils, final = [], None
    try:
        rsp = urllib.request.urlopen(r, timeout=300)
        for ligne in rsp.read().decode("utf-8", "replace").splitlines():
            if not ligne.startswith("data: "):
                continue
            try:
                e = json.loads(ligne[6:])
            except ValueError:
                continue
            if e.get("type") == "tool":
                outils.append(e.get("tool"))
            elif e.get("type") == "done":
                final = e.get("full", "")
            elif e.get("type") == "error":
                return outils, "ERREUR: " + str(e.get("error"))
    except Exception as e:
        return outils, "ERREUR: %s" % type(e).__name__
    return outils, (final or "")


# Tournures d'un modele qui ANNONCE ce qu'il va faire au lieu de le faire. Le
# cas est reel et frequent sur les petits modeles : l'outil est appele, la
# donnee revient, et la reponse finale dit « pour repondre a cette question, je
# vais consulter… » sans jamais donner le chiffre.
NARRATIONS = ["il faut", "je vais", "pour répondre à la question",
              "voici la réponse json", "je dois utiliser"]


def diagnostic(outils, texte, attendu):
    """Rend (verdict, detail). Un seul motif d'echec par tour, le plus grave."""
    if texte.startswith("ERREUR:"):
        return "ERREUR", texte[:60]
    fuites = [f for f in FUITES if f in texte]
    if fuites:
        return "FUITE", "syntaxe d'outil en clair : %s" % fuites[0]
    if attendu not in outils:
        return "SANS OUTIL", "outils appelés : %s" % (outils or "aucun")
    nu = texte.strip()
    if len(nu) < 25:
        return "TRONQUÉE", "%d caractères" % len(nu)
    # Les cinq questions de référence appellent TOUTES un nombre. Une reponse sans
    # le moindre chiffre n'a pas repondu, quelle que soit sa longueur.
    if not any(c.isdigit() for c in nu):
        return "SANS CHIFFRE", nu[:50].replace("\n", " ")
    bas = nu.lower()
    narr = [n for n in NARRATIONS if bas.startswith(n) or bas[:60].find(n) >= 0]
    if narr:
        return "NARRATION", "commence par « %s… »" % nu[:40].replace("\n", " ")
    return "OK", "%d car., %s" % (len(nu), "+".join(outils))


def principal():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tours", type=int, default=5,
                    help="nombre de répétitions par question (défaut 5)")
    args = ap.parse_args()

    try:
        sante = json.loads(urllib.request.urlopen(API + "/health", timeout=10).read())
    except Exception as e:
        print("la pile ne répond pas : %s" % e)
        return 2
    modele = sante.get("model", "?")

    print("Modèle : %s        %d tour(s) par question" % (modele, args.tours))
    print("=" * 92)
    print("%-24s %-8s %s" % ("QUESTION", "RÉUSSITE", "ÉCHECS OBSERVÉS"))
    print("-" * 92)

    total_ok = total = 0
    lignes = []

    for nom, question, attendu in QUESTIONS:
        ok = 0
        echecs = {}
        for t in range(args.tours):
            outils, texte = flux(question, "fiabilite-%s-%d" % (attendu, t))
            verdict, detail = diagnostic(outils, texte, attendu)
            if verdict == "OK":
                ok += 1
            else:
                echecs[verdict] = echecs.get(verdict, 0) + 1
        total_ok += ok
        total += args.tours
        resume = ", ".join("%s ×%d" % (k, v) for k, v in sorted(echecs.items()))
        lignes.append((nom, ok, args.tours, resume))
        print("%-24s %d/%-6d %s" % (nom, ok, args.tours, resume or "—"))

    print("-" * 92)
    pct = 100.0 * total_ok / total if total else 0
    print("%-24s %d/%-6d %.0f %%" % ("TOTAL", total_ok, total, pct))
    print()

    if pct == 100:
        print("Ce modèle tient les cinq questions de référence sur %d tours." % args.tours)
    else:
        print("Ce modèle n'est PAS fiable sur ce périmètre : %.0f %% seulement." % pct)
        print("Les échecs de type FUITE sont les plus visibles pour l'utilisateur —")
        print("la syntaxe d'appel d'outil arrive telle quelle dans la réponse.")

    # Toujours 0 : c'est une mesure, pas une barriere. Faire echouer un
    # processus parce qu'un modele local est faible n'apprend rien sur le code.
    return 0


if __name__ == "__main__":
    sys.exit(principal())
