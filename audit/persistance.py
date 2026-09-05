# -*- coding: utf-8 -*-
"""L'historique survit-il VRAIMENT à un arrêt de la pile ?

    python audit/persistance.py

La spécification l'écrit sans ambiguïté : « l'historique est persisté en base : on
arrête la stack, on la relance, tout est encore là ». C'était la seule de ses
exigences qu'aucun test ne rejouait. Je l'avais vérifiée à la main, ce qui n'est
pas la même chose : une vérification manuelle prouve que ça marchait ce jour-là.

Ce que ce script fait de plus qu'un test d'intégration ordinaire : il DÉTRUIT
les conteneurs entre les deux lectures. `docker compose down` sans `-v` retire
l'API et PostgreSQL et conserve le volume nommé. C'est le seul montage qui
distingue une vraie persistance d'un état gardé en mémoire par un processus
qu'on n'a jamais arrêté — un `restart` de l'API ne prouverait rien, PostgreSQL
n'ayant pas bougé.

⚠️ Ce script arrête la pile. Il la relance à la fin, mais toute conversation en
cours est interrompue.
"""
import json
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

# La console Windows encode en cp1252 par defaut. Ces scripts impriment du
# francais, et le premier caractere hors cp1252 — une fleche, un signe
# mathematique — tue le run EN PLEINE EXECUTION, apres avoir affiche des
# resultats verts. La CI tourne sous Linux en UTF-8 : elle ne le verra jamais.
# Une ligne par script, posee partout plutot qu'au coup par coup.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

RACINE = Path(__file__).resolve().parent.parent
API = "http://localhost:8080/api"
MOI = "audit-persistance"

# Une question dont la reponse est verifiable et qui ne coute qu'un seul appel
# au modele : le palier gratuit de certains fournisseurs plafonne a vingt par
# jour, et un script d'audit qui epuise le quota rend les suivants rouges pour
# une raison qui n'a rien a voir avec le code.
QUESTION = "Combien de stations compte le réseau au total ?"

ecarts = []
verifies = 0


def verifier(quoi, condition, detail=""):
    global verifies
    if condition:
        verifies += 1
        print("  OK    %-52s %s" % (quoi, detail))
    else:
        ecarts.append("%s — %s" % (quoi, detail))
        print("  ECART %-52s %s" % (quoi, detail))


def appel(methode, chemin, corps=None, timeout=120):
    data = json.dumps(corps).encode() if corps is not None else None
    r = urllib.request.Request(API + chemin, data=data, method=methode,
                               headers={"Content-Type": "application/json",
                                        "X-User-ID": MOI})
    try:
        rsp = urllib.request.urlopen(r, timeout=timeout)
        return rsp.status, rsp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return -1, ("%s: %s" % (type(e).__name__, e)).encode()


def compose(*args, timeout=300):
    return subprocess.run(["docker", "compose", *args], cwd=RACINE,
                          capture_output=True, text=True, timeout=timeout,
                          encoding="utf-8", errors="replace")


def attendre_sante(limite=180):
    """Rend True des que /health repond ok, sinon False au bout de `limite` s."""
    fin = time.time() + limite
    while time.time() < fin:
        st, b = appel("GET", "/health", timeout=5)
        if st == 200:
            try:
                if json.loads(b).get("status") == "ok":
                    return True
            except ValueError:
                pass
        time.sleep(2)
    return False


print("=== Avant l'arrêt ===")

if not attendre_sante(30):
    print("  la pile ne répond pas : lancer `docker compose up -d` d'abord")
    sys.exit(2)

st, b = appel("POST", "/conversations", {})
if st not in (200, 201):
    print("  création impossible : statut %s" % st)
    sys.exit(2)
conv = json.loads(b)["id"]
print("  conversation %s" % conv)

st, b = appel("POST", "/conversations/%s/messages" % conv, {"message": QUESTION})

# La reponse arrive en SSE. On ne garde que l'evenement final, le seul qui porte
# la reponse complete.
final = None
for ligne in b.decode("utf-8", "replace").splitlines():
    if ligne.startswith("data: "):
        try:
            ev = json.loads(ligne[6:])
        except ValueError:
            continue
        if ev.get("type") == "done":
            final = ev

reponse = ((final or {}).get("full") or "").strip()

# ── Deux modes, et le script DIT lequel il exécute ──────────────────────────
#
# La CI lance la pile avec une cle factice : aucun appel au modele n'aboutit.
# Un script qui exigerait une vraie reponse y rougirait pour une raison qui n'a
# rien a voir avec la persistance, et un rouge qui ne dit rien sur le code est
# celui qu'on apprend a ignorer.
#
# Sans modele, on verifie quand meme ce qui compte le plus : le volume nomme,
# le branchement PostgreSQL et la survie de la conversation a la destruction
# des conteneurs. Ce qu'on perd, c'est la preuve que le CONTENU des messages
# survit — alors on l'annonce, au lieu de rendre un vert qui laisserait croire
# que tout a ete verifie.
avec_modele = st == 200 and len(reponse) > 10

if avec_modele:
    print("  mode COMPLET : le modèle a répondu, on vérifiera le contenu")
    # ⚠️ On ne vérifie PAS que la réponse est juste, et c'est délibéré.
    #
    # La version précédente exigeait que la réponse cite « 1 519 ». Elle est
    # passée au rouge le jour où un modèle local a répondu par un fragment de
    # schéma JSON — un échec réel, mais du MODÈLE, pas de la persistance. Un
    # test qui rougit pour une raison étrangère à son objet est un test qu'on
    # apprend à ignorer, et c'est ainsi qu'on rate le vrai rouge.
    #
    # La justesse des réponses a son propre script : audit/evaluation.py.
    # Ici, seule compte la question suivante — ce qui a été écrit est-il
    # toujours là après la destruction des conteneurs ?
    print("  (la justesse de la réponse relève de evaluation.py, pas d'ici)")
else:
    print("  ⚠️  mode DÉGRADÉ : le modèle n'a pas répondu (statut %s)." % st)
    print("      La persistance de la conversation est vérifiée, celle du")
    print("      CONTENU des messages ne l'est pas. C'est le mode de la CI,")
    print("      qui démarre la pile avec une clé factice.")

st, b = appel("GET", "/conversations/" + conv)
avant = json.loads(b)

if avec_modele:
    verifier("l'historique contient la question et la réponse",
             avant["message_count"] >= 2,
             "%d messages" % avant["message_count"])
    verifier("le titre a été dérivé de la question",
             avant["title"] != "Nouvelle conversation",
             repr(avant["title"]))

print()
print("=== On détruit les conteneurs ===")
print("  docker compose down   (SANS -v : le volume nommé doit survivre)")

r = compose("down")
if r.returncode != 0:
    print("  échec de l'arrêt : %s" % (r.stderr or r.stdout)[:300])
    sys.exit(2)

# La preuve que l'arret a bien eu lieu : plus rien ne repond. Sans cette
# verification, un `down` qui echouerait en silence ferait passer le test pour
# la meilleure des mauvaises raisons — on relirait la donnee du processus qui
# n'a jamais ete arrete.
st, _ = appel("GET", "/health", timeout=5)
verifier("l'API ne répond plus", st == -1, "statut %s" % st)

# Et que le volume, lui, est toujours la.
r = compose("ps", "-aq")
verifier("plus aucun conteneur du projet", r.stdout.strip() == "",
         "%d ligne(s)" % len(r.stdout.split()))

print()
print("=== On relance ===")
r = compose("up", "-d", timeout=600)
if r.returncode != 0:
    print("  échec du redémarrage : %s" % (r.stderr or r.stdout)[:300])
    sys.exit(2)

if not attendre_sante(180):
    print("  la pile n'est pas revenue en bonne santé")
    sys.exit(1)
print("  la pile répond de nouveau")

print()
print("=== Après le redémarrage ===")

st, b = appel("GET", "/conversations/" + conv)
verifier("la conversation est toujours lisible", st == 200, "statut %s" % st)
if st != 200:
    print("\nL'historique n'a pas survécu : c'est une exigence explicite de la spécification.")
    sys.exit(1)

apres = json.loads(b)

verifier("même identifiant", apres["id"] == avant["id"], apres["id"])
verifier("même titre", apres["title"] == avant["title"], repr(apres["title"]))
verifier("même nombre de messages",
         apres["message_count"] == avant["message_count"],
         "%d avant, %d après" % (avant["message_count"], apres["message_count"]))
verifier("même date de création",
         apres["created_at"] == avant["created_at"], apres["created_at"])

# Le coeur du problème : c'est le CONTENU qui doit survivre, pas seulement le
# compteur. Une ligne de metadonnees conservee avec des messages vides
# passerait toutes les verifications precedentes.
textes_avant = [m.get("content", "") for m in avant.get("messages", [])]
textes_apres = [m.get("content", "") for m in apres.get("messages", [])]

if avec_modele:
    verifier("le contenu des messages est identique",
             textes_avant == textes_apres and textes_avant != [],
             "%d message(s) comparés" % len(textes_avant))
    verifier("la question posée est encore là",
             any(QUESTION in t for t in textes_apres), QUESTION[:40])
    verifier("la réponse du modèle est encore là",
             any(reponse[:40] in t for t in textes_apres),
             reponse[:40].replace("\n", " "))
else:
    # Meme sans modele, l'egalite doit tenir : deux listes vides sont egales,
    # mais une liste qui se remplirait ou se viderait au redemarrage ne le
    # serait pas.
    verifier("le tableau des messages est inchangé",
             textes_avant == textes_apres,
             "%d avant, %d après" % (len(textes_avant), len(textes_apres)))

st, b = appel("GET", "/conversations")
liste = json.loads(b)
items = liste if isinstance(liste, list) else liste.get("conversations", [])
verifier("la conversation apparaît encore dans la liste",
         any(c.get("id") == conv for c in items),
         "%d conversation(s) listée(s)" % len(items))

# Menage : le test ne doit pas laisser sa trace derriere lui.
appel("DELETE", "/conversations/" + conv)

print()
print("-" * 78)
if ecarts:
    print("%d écart(s) :" % len(ecarts))
    for e in ecarts:
        print("   - " + e)
    sys.exit(1)
print("%d vérifications — l'historique survit à la destruction des conteneurs%s"
      % (verifies, "" if avec_modele else "  (mode DÉGRADÉ, sans le contenu)"))
sys.exit(0)
