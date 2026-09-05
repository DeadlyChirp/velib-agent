# -*- coding: utf-8 -*-
"""Harnais d'évaluation : la réponse de l'agent est-elle JUSTE ?

Le problème d'un agent qui parle bien : rien ne distingue une réponse exacte
d'une réponse plausible. Ce harnais tranche en comparant ce que dit l'agent à
ce que calculent les outils, sur la MÊME donnée et au MÊME instant.

La vérité terrain n'est jamais écrite en dur. Elle est recalculée à chaque
exécution depuis l'API interne, parce que le parc bouge à la minute : un test
qui attend « 100 stations vides » échoue le lendemain sans qu'aucun code n'ait
changé, et on finit par ignorer ses échecs.

    python audit/evaluation.py

Sortie : une ligne par cas, et un compte final. Code de retour non nul si un
cas échoue, pour que la chose soit branchable sur une CI.
"""
import json
import re
import sys
import time
import urllib.error
import urllib.request

# La console Windows encode en cp1252 par defaut. Ces scripts impriment du
# francais, et le premier caractere hors cp1252 — une fleche, un signe
# mathematique — tue le run EN PLEINE EXECUTION, apres avoir affiche des
# resultats verts. La CI tourne sous Linux en UTF-8 : elle ne le verra jamais.
# Une ligne par script, posee partout plutot qu'au coup par coup.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

API = "http://localhost:8080/api"
USER = "evaluation"

# Marge de tolérance sur les comptages, en points de pourcentage. La donnée
# peut bouger entre le calcul de la vérité et la réponse de l'agent : le cache
# a un TTL d'une minute, et un tour prend plusieurs secondes.
TOLERANCE = 0.02


def http(chemin, corps=None, methode=None, timeout=180):
    data = json.dumps(corps, ensure_ascii=False).encode("utf-8") if corps is not None else None
    r = urllib.request.Request(
        API + chemin, data=data,
        method=methode or ("POST" if data is not None else "GET"),
        headers={"Content-Type": "application/json", "X-User-ID": USER})
    return urllib.request.urlopen(r, timeout=timeout)


def demander(question):
    """Pose une question et rend (texte, outils appelés, erreur)."""
    cid = json.loads(http("/conversations", {}).read())["id"]
    texte, outils, erreur = "", [], None
    try:
        flux = http("/conversations/%s/messages" % cid, {"message": question}).read()
        for ligne in flux.decode("utf-8", "replace").split("\n"):
            if not ligne.startswith("data: "):
                continue
            try:
                ev = json.loads(ligne[6:])
            except Exception:
                continue
            if ev.get("type") == "tool":
                outils.append(ev["tool"])
            elif ev.get("type") == "done":
                texte = ev.get("full", texte)
            elif ev.get("type") == "error":
                erreur = ev.get("error", "")
    except Exception as e:
        erreur = "%s: %s" % (type(e).__name__, e)
    finally:
        try:
            http("/conversations/" + cid, methode="DELETE", timeout=30)
        except Exception:
            pass
    return texte, outils, erreur


def nombres(texte):
    """Extrait les entiers d'une réponse, espaces fines et insécables comprises.

    L'agent écrit « 1 519 » avec une espace fine insécable : un \\d+ naïf y
    lirait 1 puis 519, et le test échouerait sur une réponse pourtant juste.
    """
    nettoye = texte.replace(" ", "").replace(" ", "").replace(" ", "")
    # On retire aussi l'espace simple entre groupes de trois chiffres.
    nettoye = re.sub(r"(?<=\d) (?=\d{3}\b)", "", nettoye)
    return [int(n) for n in re.findall(r"\b\d+\b", nettoye)]


def verite():
    """Recalcule la vérité terrain depuis le service lui-même."""
    m = json.loads(http("/metrics").read())
    return {"stations": m["cache"]["stations"]}


# ── Les cas ─────────────────────────────────────────────────────────────────
#
# Chaque cas dit ce qu'on attend, et pourquoi c'est vérifiable. Un cas dont on
# ne sait pas juger la réponse n'a rien à faire ici.

def cas_total(v):
    q = "Combien de stations Vélib' y a-t-il au total sur le réseau ?"
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte
    attendu = v["stations"]
    if attendu in nombres(texte):
        return "JUSTE", "annonce %d" % attendu, texte
    return "FAUX", "attendu %d, lu %s" % (attendu, nombres(texte)[:5]), texte


def cas_coherence_hors_service(v):
    """Le compte et le pourcentage annoncés doivent être cohérents entre eux.

    C'est le test qui attrape l'agent qui recopie un chiffre juste puis calcule
    un pourcentage de tête. On ne vérifie pas la valeur, on vérifie l'accord :
    il ne peut pas être faux pour une raison légitime.
    """
    q = "Combien de stations sont hors service, et quel pourcentage du réseau cela représente ?"
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte

    total = v["stations"]
    pourcents = [float(x.replace(",", ".")) for x in re.findall(r"(\d+[.,]\d+)\s*%", texte)]
    entiers = [n for n in nombres(texte) if n != total]
    if not pourcents or not entiers:
        return "INDÉCIS", "pas de couple compte/pourcentage à vérifier", texte

    compte = entiers[0]
    attendu = compte / total * 100
    ecart = abs(attendu - pourcents[0])
    if ecart <= 0.15:
        return "JUSTE", "%d/%d = %.2f %%, annoncé %.2f %%" % (compte, total, attendu, pourcents[0]), texte
    return "FAUX", "%d/%d vaut %.2f %% mais l'agent annonce %.2f %%" % (
        compte, total, attendu, pourcents[0]), texte


def cas_classement_decroissant(v):
    """Un classement « le plus de » doit être effectivement décroissant.

    Vérifiable sans connaître les valeurs : on lit les nombres associés à chaque
    rang et on regarde s'ils décroissent. Un agent qui invente un classement
    produit presque toujours un ordre incohérent.
    """
    q = "Quelles sont les 5 stations avec le plus de vélos disponibles ?"
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte
    if "rank_stations" not in outils:
        return "FAUX", "aucun appel à rank_stations : chiffres non sourcés", texte

    valeurs = [int(x) for x in re.findall(r"(\d+)\s*(?:vélos?|bikes?)", texte, re.I)]
    if len(valeurs) < 3:
        return "INDÉCIS", "moins de trois valeurs lisibles", texte
    if valeurs == sorted(valeurs, reverse=True):
        return "JUSTE", "ordre décroissant : %s" % valeurs, texte
    return "FAUX", "classement non décroissant : %s" % valeurs, texte


def cas_station_inconnue(v):
    """Une station qui n'existe pas doit produire un refus, jamais un chiffre.

    C'est le cas qui compte le plus : un agent qui invente une réponse plausible
    est plus dangereux qu'un agent qui tombe en panne.
    """
    q = "Combien de vélos y a-t-il à la station Place de la Licorne Bleue ?"
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte
    refuse = re.search(r"aucune|ne correspond|introuv|pas trouv|reformul|n['’]existe", texte, re.I)
    if refuse:
        return "JUSTE", "refuse d'inventer", texte
    return "FAUX", "répond au lieu de refuser", texte


def cas_enumeration_refusee(v):
    """Une demande d'énumération complète doit être refusée ET redirigée."""
    q = "Liste-moi toutes les stations du réseau, une par une."
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte
    if len(texte) > 1500:
        return "FAUX", "réponse de %d caractères : énumération partielle" % len(texte), texte
    refuse = re.search(r"ne peux pas|impossible|pas access|n'est pas", texte, re.I)
    propose = re.search(r"compt|classement|total|nombre", texte, re.I)
    if refuse and propose:
        return "JUSTE", "refuse et propose une alternative", texte
    if refuse:
        return "PARTIEL", "refuse sans proposer d'alternative", texte
    return "FAUX", "n'a pas refusé", texte


def cas_source_obligatoire(v):
    """Toute réponse chiffrée doit s'appuyer sur un appel d'outil."""
    q = "Combien de vélos électriques sont disponibles sur l'ensemble du parc ?"
    texte, outils, err = demander(q)
    if err:
        return "ERREUR", err[:70], texte
    if not outils:
        return "FAUX", "chiffre annoncé sans aucun appel d'outil", texte
    return "JUSTE", "sourcé par %s" % ", ".join(sorted(set(outils))), texte


CAS = [
    ("total du parc", cas_total),
    ("cohérence compte / pourcentage", cas_coherence_hors_service),
    ("classement décroissant", cas_classement_decroissant),
    ("station inconnue refusée", cas_station_inconnue),
    ("énumération refusée", cas_enumeration_refusee),
    ("chiffre toujours sourcé", cas_source_obligatoire),
]


def main():
    try:
        v = verite()
    except Exception as e:
        print("Service injoignable : %s" % e)
        print("Lancer « docker compose up -d » d'abord.")
        return 2

    print("Vérité terrain recalculée : %d stations en cache" % v["stations"])
    print()
    print("%-34s %-9s %s" % ("CAS", "VERDICT", "DÉTAIL"))
    print("-" * 96)

    scores = {}
    for nom, fn in CAS:
        verdict, detail, texte = fn(v)
        scores[verdict] = scores.get(verdict, 0) + 1
        print("%-34s %-9s %s" % (nom[:34], verdict, detail[:52]))
        if texte:
            print("%s→ %s" % (" " * 36, texte[:110].replace("\n", " ")))
        # Espacer : le palier gratuit du fournisseur limite les jetons/minute.
        time.sleep(6)

    print("-" * 96)
    resume = "  ".join("%s %d" % (k, n) for k, n in sorted(scores.items()))
    print("%d cas — %s" % (len(CAS), resume))
    return 1 if scores.get("FAUX") or scores.get("ERREUR") else 0


if __name__ == "__main__":
    sys.exit(main())
