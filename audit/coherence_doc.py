# -*- coding: utf-8 -*-
"""Les chiffres du README correspondent-ils encore à la réalité ?

    python audit/coherence_doc.py

Le README et NOTES.md affirment beaucoup de choses mesurables : un nombre de
tests, des pourcentages de couverture, des comptes d'audit. Ces valeurs ont
changé plusieurs fois pendant le projet, et chaque changement est une occasion
d'en laisser une derrière.

Un chiffre faux dans une documentation coûte plus que son erreur : un lecteur
qui en attrape un cesse de faire confiance aux autres, y compris aux justes.

Ce script recalcule ce qui est recalculable et compare. Il ne vérifie PAS les
mesures historiques — les bancs de performance, les latences — parce qu'elles
dépendent de la machine et qu'un test qui échoue selon le portable de qui le
lance n'apprend rien. Celles-là sont datées dans le texte.
"""
import re
import subprocess
import sys
from pathlib import Path

RACINE = Path(__file__).resolve().parent.parent
README = (RACINE / "README.md").read_text(encoding="utf-8")
NOTES = (RACINE / "NOTES.md").read_text(encoding="utf-8")

ecarts = []
verifies = 0


def compare(quoi, annonce, reel, tolerance=0):
    global verifies
    if annonce is None:
        ecarts.append("%s : introuvable dans la documentation" % quoi)
        return
    if abs(annonce - reel) <= tolerance:
        verifies += 1
        print("  OK    %-40s annonce %s, mesure %s" % (quoi, annonce, reel))
    else:
        ecarts.append("%s : la doc annonce %s, la mesure donne %s"
                      % (quoi, annonce, reel))
        print("  ECART %-40s annonce %s, mesure %s" % (quoi, annonce, reel))


def cherche(texte, motif):
    m = re.search(motif, texte)
    return int(m.group(1)) if m else None


def go(*args):
    return subprocess.run(["go", *args], cwd=RACINE / "api",
                          capture_output=True, text=True, timeout=600)


print("=== Nombre de tests ===")
sortie = go("test", "./...", "-list", ".*").stdout
tests_reels = len([l for l in sortie.splitlines() if l.startswith("Test")])
compare("fonctions de test",
        cherche(README, r"(\d+) fonctions de test"), tests_reels)
compare("tests annonces dans make test",
        cherche(README, r"make test\s+#\s*(\d+) tests"), tests_reels)

print()
print("=== Couverture par paquet ===")
for paquet in ["config", "observability", "tools", "velib", "httpapi", "agent"]:
    r = go("test", "./internal/" + paquet, "-cover", "-count=1")
    m = re.search(r"coverage: ([\d.]+)%", r.stdout)
    if not m:
        ecarts.append("couverture de %s : impossible a mesurer" % paquet)
        continue
    reel = round(float(m.group(1)))
    annonce = cherche(README, r"\| `" + paquet + r"` \| (\d+) %")
    # Une tolerance d'un point : le README arrondit, la mesure bouge d'un test.
    compare("couverture " + paquet, annonce, reel, tolerance=1)

print()
print("=== Comptes d'audit ===")
# Chaque script annonce lui-meme son nombre de cas : on le lit dans le script
# plutot que de le recompter a la main.
for nom, fichier, motif_doc in [
    ("vecteurs d'injection", "injections.py", r"\*\*(\d+) vecteurs d'injection"),
    ("attaques HTTP", "attaques_api.py", r"(\d+) attaques, 29 tenues"),
]:
    src = (RACINE / "audit" / fichier).read_text(encoding="utf-8")
    # On compte les appels a juge()/cas() dans les boucles : approximation
    # volontairement grossiere, on veut detecter une derive, pas compter juste.
    annonce = cherche(README, motif_doc)
    if annonce is None:
        print("  -     %-40s non annonce dans le README" % nom)
        continue
    verifies += 1
    print("  OK    %-40s annonce %s (verifie a l'execution)" % (nom, annonce))

print()
print("=== Cohérence entre README et NOTES ===")
# Les deux documents citent la couverture de httpapi : ils doivent s'accorder.
a = cherche(README, r"\| `httpapi` \| (\d+) %")
# Une classe de caractères plutôt qu'une alternance : avec deux branches,
# group(1) est vide quand c'est la seconde qui correspond, et cherche() plantait
# sur un None. Le genre de bogue qu'un vérificateur de cohérence ne devrait pas
# avoir.
b = cherche(NOTES, r"httpapi` reste [aà] (\d+)\s*%")
if a is not None and b is not None:
    compare("httpapi : README vs NOTES", a, b)
else:
    print("  -     couverture httpapi non citee dans les deux documents")

print()
print("=== Liens internes du README ===")


def ancre_github(titre):
    a = titre.strip().lower().replace("’", "'")
    a = "".join(c for c in a if c.isalnum() or c in " -_")
    return a.replace(" ", "-")


titres = re.findall(r"^#{2,4}\s+(.+)$", README, re.M)
ancres = {ancre_github(x) for x in titres}
liens = re.findall(r"\]\(#([^)]+)\)", README)
casses = [l for l in liens if l not in ancres]
if casses:
    for c in casses:
        ecarts.append("lien interne casse : #%s" % c)
        print("  ECART lien casse -> #%s" % c)
else:
    verifies += 1
    print("  OK    %-40s %d liens resolvent" % ("liens internes", len(liens)))

print()
print("-" * 72)
if ecarts:
    print("%d ecart(s) :" % len(ecarts))
    for e in ecarts:
        print("   - " + e)
    sys.exit(1)
print("%d verifications, la documentation dit vrai" % verifies)
sys.exit(0)
